package governance

import (
	"fmt"
	"math/bits"
)

// ProposalState is the lifecycle state of a governance proposal.
type ProposalState int

const (
	StateActive ProposalState = iota
	StateDefeated
	StateSucceeded
	StateQueued
	StateExecuted
	StateCancelled
	StateExpired
)

func (s ProposalState) String() string {
	switch s {
	case StateActive:
		return "Active"
	case StateDefeated:
		return "Defeated"
	case StateSucceeded:
		return "Succeeded"
	case StateQueued:
		return "Queued"
	case StateExecuted:
		return "Executed"
	case StateCancelled:
		return "Cancelled"
	case StateExpired:
		return "Expired"
	default:
		return "Unknown"
	}
}

// Proposal is a single governance proposal. Voting weight is always read
// from the ledger at SnapshotBlock, fixed at creation time.
type Proposal struct {
	ID            uint64
	Title         string
	Proposer      string
	SnapshotBlock uint64
	StartBlock    uint64
	EndBlock      uint64
	ForVotes      uint64
	AgainstVotes  uint64
	Eta           uint64 // set once queued into the timelock
	TxID          string // timelock transaction id, once queued

	voted map[string]bool
	state ProposalState
}

// mulDivCeil computes ceil(a*b/d) using 128-bit intermediate arithmetic.
func mulDivCeil(a, b, d uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	q, r := bits.Div64(hi, lo, d)
	if r != 0 {
		q++
	}
	return q
}

// mulCmp compares a*b with c*d using 128-bit arithmetic, returning -1, 0 or 1.
func mulCmp(a, b, c, d uint64) int {
	hi1, lo1 := bits.Mul64(a, b)
	hi2, lo2 := bits.Mul64(c, d)
	if hi1 != hi2 {
		if hi1 < hi2 {
			return -1
		}
		return 1
	}
	if lo1 != lo2 {
		if lo1 < lo2 {
			return -1
		}
		return 1
	}
	return 0
}

// Governor manages proposals, snapshot-weighted voting, quorum/approval
// evaluation, and the hand-off of succeeded proposals to the timelock.
//
// Quorum and approval are evaluated with exact integer arithmetic:
//
//	quorumRequired = ceil(totalSupply * QuorumBps / 10000)
//	quorumReached  = (forVotes + againstVotes) >= quorumRequired
//	approvalReached = forVotes * 10000 >= ApprovalBps * (forVotes + againstVotes)
//
// Both boundaries are inclusive: participation exactly equal to the quorum
// requirement passes, and an approval ratio exactly equal to the threshold
// passes.
type Governor struct {
	ledger   *Ledger
	timelock *Timelock

	VotingDelay  uint64 // blocks between creation and voting start
	VotingPeriod uint64 // blocks voting stays open
	QuorumBps    uint64 // quorum as basis points of total supply
	ApprovalBps  uint64 // approval threshold as basis points of votes cast

	proposals map[uint64]*Proposal
	nextID    uint64
}

func NewGovernor(ledger *Ledger, timelock *Timelock, votingDelay, votingPeriod, quorumBps, approvalBps uint64) *Governor {
	return &Governor{
		ledger:       ledger,
		timelock:     timelock,
		VotingDelay:  votingDelay,
		VotingPeriod: votingPeriod,
		QuorumBps:    quorumBps,
		ApprovalBps:  approvalBps,
		proposals:    make(map[uint64]*Proposal),
		nextID:       1,
	}
}

// Propose creates a proposal, pinning the current block as the voting
// weight snapshot. Voting is open for blocks [start, end] inclusive.
func (g *Governor) Propose(block uint64, proposer, title string) uint64 {
	id := g.nextID
	g.nextID++
	g.proposals[id] = &Proposal{
		ID:            id,
		Title:         title,
		Proposer:      proposer,
		SnapshotBlock: block,
		StartBlock:    block + g.VotingDelay,
		EndBlock:      block + g.VotingDelay + g.VotingPeriod,
		voted:         make(map[string]bool),
		state:         StateActive,
	}
	return id
}

// CastVote records a vote weighted by the voter's balance at the
// proposal's snapshot block. Each account votes at most once, and an
// account with zero weight at the snapshot cannot vote at all — tokens
// acquired after the snapshot never count.
func (g *Governor) CastVote(block, id uint64, voter string, support bool) (uint64, error) {
	p, ok := g.proposals[id]
	if !ok {
		return 0, fmt.Errorf("governor: proposal %d not found", id)
	}
	if p.state != StateActive {
		return 0, fmt.Errorf("governor: proposal %d is %s, voting is closed", id, p.state)
	}
	if block < p.StartBlock || block > p.EndBlock {
		return 0, fmt.Errorf("governor: voting window for proposal %d is [%d,%d], now=%d", id, p.StartBlock, p.EndBlock, block)
	}
	if p.voted[voter] {
		return 0, fmt.Errorf("governor: %s already voted on proposal %d", voter, id)
	}
	weight := g.ledger.GetPriorVotes(voter, p.SnapshotBlock)
	if weight == 0 {
		return 0, fmt.Errorf("governor: %s has zero voting weight at snapshot block %d", voter, p.SnapshotBlock)
	}
	p.voted[voter] = true
	if support {
		p.ForVotes += weight
	} else {
		p.AgainstVotes += weight
	}
	return weight, nil
}

// QuorumRequired returns ceil(totalSupply * QuorumBps / 10000).
func (g *Governor) QuorumRequired() uint64 {
	return mulDivCeil(g.ledger.TotalSupply(), g.QuorumBps, 10000)
}

func participation(p *Proposal) uint64 { return p.ForVotes + p.AgainstVotes }

// QuorumReached reports whether participation meets the quorum requirement.
// The boundary is inclusive: exactly equal passes.
func (g *Governor) QuorumReached(p *Proposal) bool {
	return participation(p) >= g.QuorumRequired()
}

// ApprovalReached reports whether forVotes/(forVotes+againstVotes) meets
// the approval threshold, compared exactly in 128-bit integer arithmetic.
// The boundary is inclusive.
func (g *Governor) ApprovalReached(p *Proposal) bool {
	votes := participation(p)
	if votes == 0 {
		return false
	}
	return mulCmp(p.ForVotes, 10000, g.ApprovalBps, votes) >= 0
}

// Finalize closes an active proposal whose voting period has ended and
// records the outcome: Succeeded only if both quorum and approval pass.
func (g *Governor) Finalize(block, id uint64) (ProposalState, error) {
	p, ok := g.proposals[id]
	if !ok {
		return 0, fmt.Errorf("governor: proposal %d not found", id)
	}
	if p.state != StateActive {
		return p.state, fmt.Errorf("governor: proposal %d already finalized (%s)", id, p.state)
	}
	if block <= p.EndBlock {
		return p.state, fmt.Errorf("governor: proposal %d voting open until block %d, now=%d", id, p.EndBlock, block)
	}
	if g.QuorumReached(p) && g.ApprovalReached(p) {
		p.state = StateSucceeded
	} else {
		p.state = StateDefeated
	}
	return p.state, nil
}

// Queue hands a succeeded proposal to the timelock and returns the eta.
// An active proposal past its voting period is finalized first.
func (g *Governor) Queue(block, id uint64) (uint64, error) {
	p, ok := g.proposals[id]
	if !ok {
		return 0, fmt.Errorf("governor: proposal %d not found", id)
	}
	if p.state == StateActive {
		if _, err := g.Finalize(block, id); err != nil {
			return 0, err
		}
	}
	if p.state != StateSucceeded {
		return 0, fmt.Errorf("governor: proposal %d is %s, only Succeeded proposals can be queued", id, p.state)
	}
	txID := fmt.Sprintf("proposal-%d", id)
	eta, err := g.timelock.Queue(block, txID)
	if err != nil {
		return 0, err
	}
	p.TxID = txID
	p.Eta = eta
	p.state = StateQueued
	return eta, nil
}

// Cancel lets the proposer cancel a proposal that has not been executed.
// For a queued proposal the timelock record is invalidated too, so the
// queued transaction can never fire inside a later execution window.
func (g *Governor) Cancel(block, id uint64, caller string) error {
	p, ok := g.proposals[id]
	if !ok {
		return fmt.Errorf("governor: proposal %d not found", id)
	}
	if caller != p.Proposer {
		return fmt.Errorf("governor: only proposer %s can cancel proposal %d (got %s)", p.Proposer, id, caller)
	}
	switch p.state {
	case StateActive, StateSucceeded:
		p.state = StateCancelled
		return nil
	case StateQueued:
		if err := g.timelock.Cancel(block, p.TxID); err != nil {
			return err
		}
		p.state = StateCancelled
		return nil
	default:
		return fmt.Errorf("governor: proposal %d is %s, cannot cancel", id, p.state)
	}
}

// Execute runs a queued proposal inside its timelock execution window.
func (g *Governor) Execute(block, id uint64) error {
	p, ok := g.proposals[id]
	if !ok {
		return fmt.Errorf("governor: proposal %d not found", id)
	}
	if p.state != StateQueued {
		return fmt.Errorf("governor: proposal %d is %s, cannot execute", id, p.state)
	}
	if err := g.timelock.Execute(block, p.TxID); err != nil {
		return err
	}
	p.state = StateExecuted
	return nil
}

// Requeue gives an expired queued proposal a fresh eta.
func (g *Governor) Requeue(block, id uint64) (uint64, error) {
	p, ok := g.proposals[id]
	if !ok {
		return 0, fmt.Errorf("governor: proposal %d not found", id)
	}
	if p.state != StateQueued {
		return 0, fmt.Errorf("governor: proposal %d is %s, cannot requeue", id, p.state)
	}
	eta, err := g.timelock.Requeue(block, p.TxID)
	if err != nil {
		return 0, err
	}
	p.Eta = eta
	return eta, nil
}

// State returns the proposal's state as of the given block, deriving
// outcomes and expiry from the clock without mutating anything.
func (g *Governor) State(block, id uint64) (ProposalState, error) {
	p, ok := g.proposals[id]
	if !ok {
		return 0, fmt.Errorf("governor: proposal %d not found", id)
	}
	if p.state == StateActive && block > p.EndBlock {
		if g.QuorumReached(p) && g.ApprovalReached(p) {
			return StateSucceeded, nil
		}
		return StateDefeated, nil
	}
	if p.state == StateQueued && g.timelock.Status(block, p.TxID) == TxExpired {
		return StateExpired, nil
	}
	return p.state, nil
}

// Proposal returns the proposal with the given id.
func (g *Governor) Proposal(id uint64) (*Proposal, bool) {
	p, ok := g.proposals[id]
	return p, ok
}

// Proposals returns all proposals ordered by id.
func (g *Governor) Proposals() []*Proposal {
	out := make([]*Proposal, 0, len(g.proposals))
	for id := uint64(1); id < g.nextID; id++ {
		out = append(out, g.proposals[id])
	}
	return out
}

// Timelock exposes the underlying timelock (read-only usage intended).
func (g *Governor) Timelock() *Timelock { return g.timelock }

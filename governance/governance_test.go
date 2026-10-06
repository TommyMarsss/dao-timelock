package governance

import (
	"strings"
	"testing"
)

// newTestEnv builds a ledger with a fixed initial distribution and a
// governor with votingDelay=2, votingPeriod=10, quorum=40%, approval=50%.
func newTestEnv() (*Ledger, *Timelock, *Governor) {
	l := NewLedger()
	l.Mint(1, "alice", 400)
	l.Mint(1, "bob", 300)
	l.Mint(1, "carol", 200)
	l.Mint(1, "dave", 100) // total supply 1000
	tl := NewTimelock(20, 10)
	g := NewGovernor(l, tl, 2, 10, 4000, 5000)
	return l, tl, g
}

// --- 1. Snapshot: balance changes after the snapshot must not affect weight ---

func TestSnapshotIgnoresPostSnapshotBalanceChanges(t *testing.T) {
	l, _, g := newTestEnv()
	id := g.Propose(5, "alice", "snapshot test") // snapshot block = 5

	// Alice votes with her snapshot weight of 400.
	w, err := g.CastVote(7, id, "alice", true)
	if err != nil {
		t.Fatalf("alice vote: %v", err)
	}
	if w != 400 {
		t.Fatalf("alice weight = %d, want 400", w)
	}

	// Alice acquires 500 more tokens AFTER the snapshot...
	l.Mint(8, "alice", 500)
	// ...and tries to vote again. It must be rejected outright.
	if _, err := g.CastVote(9, id, "alice", true); err == nil {
		t.Fatal("second vote by alice must be rejected")
	}

	// Eve had 0 tokens at the snapshot; she receives 500 afterwards.
	// Her vote must be rejected: zero weight at the snapshot block.
	l.Mint(8, "eve", 500)
	if _, err := g.CastVote(9, id, "eve", true); err == nil {
		t.Fatal("eve must not vote with tokens acquired after the snapshot")
	}

	// A transfer after the snapshot must not move weight either:
	// bob sends his 300 to carol at block 9; bob still votes with 300
	// (his snapshot balance), carol keeps her snapshot 200, not 500.
	if err := l.Transfer(9, "bob", "carol", 300); err != nil {
		t.Fatal(err)
	}
	w, err = g.CastVote(10, id, "bob", true)
	if err != nil || w != 300 {
		t.Fatalf("bob vote: w=%d err=%v, want weight 300", w, err)
	}
	w, err = g.CastVote(10, id, "carol", false)
	if err != nil || w != 200 {
		t.Fatalf("carol vote: w=%d err=%v, want weight 200 (snapshot), not 500", w, err)
	}

	p, _ := g.Proposal(id)
	if p.ForVotes != 700 || p.AgainstVotes != 200 {
		t.Fatalf("tally = %d for / %d against, want 700/200", p.ForVotes, p.AgainstVotes)
	}
	// Sanity: current balances did move; only the snapshot is frozen.
	if l.BalanceOf("carol") != 500 {
		t.Fatalf("carol current balance = %d, want 500", l.BalanceOf("carol"))
	}
	if got := l.GetPriorVotes("carol", 5); got != 200 {
		t.Fatalf("carol snapshot weight = %d, want 200", got)
	}
}

func TestGetPriorVotesBoundaries(t *testing.T) {
	l := NewLedger()
	l.Mint(10, "alice", 100)
	l.Mint(20, "alice", 50)
	l.Transfer(30, "alice", "bob", 40)

	cases := []struct {
		block uint64
		want  uint64
	}{
		{9, 0},    // before first checkpoint
		{10, 100}, // exact checkpoint block
		{15, 100}, // between checkpoints
		{20, 150},
		{29, 150},
		{30, 110}, // after transfer out
		{100, 110},
	}
	for _, c := range cases {
		if got := l.GetPriorVotes("alice", c.block); got != c.want {
			t.Errorf("GetPriorVotes(alice, %d) = %d, want %d", c.block, got, c.want)
		}
	}
	if got := l.GetPriorVotes("bob", 30); got != 40 {
		t.Errorf("GetPriorVotes(bob, 30) = %d, want 40", got)
	}
}

// --- 2. Quorum and approval threshold, including exact boundaries ---

func TestQuorumBoundaryExact(t *testing.T) {
	_, _, g := newTestEnv() // supply 1000, quorum 40% -> required = 400
	if got := g.QuorumRequired(); got != 400 {
		t.Fatalf("QuorumRequired = %d, want 400", got)
	}

	// Participation exactly 400 (alice alone) -> quorum reached.
	id := g.Propose(5, "alice", "exact quorum")
	g.CastVote(7, id, "alice", true)
	p, _ := g.Proposal(id)
	if !g.QuorumReached(p) {
		t.Fatal("participation 400 == required 400 must reach quorum (inclusive boundary)")
	}
	st, err := g.Finalize(18, id)
	if err != nil || st != StateSucceeded {
		t.Fatalf("finalize = %s, %v; want Succeeded", st, err)
	}

	// Participation 399 (bob 300 + dave 99? use carol 200 + dave 100 + 99...)
	// Simpler: carol(200) + dave(100) = 300 < 400 -> not reached.
	id2 := g.Propose(5, "bob", "below quorum")
	g.CastVote(7, id2, "carol", true)
	g.CastVote(7, id2, "dave", true)
	p2, _ := g.Proposal(id2)
	if g.QuorumReached(p2) {
		t.Fatal("participation 300 < 400 must not reach quorum")
	}
	st, _ = g.Finalize(18, id2)
	if st != StateDefeated {
		t.Fatalf("below-quorum proposal = %s, want Defeated", st)
	}
}

func TestQuorumRoundsUp(t *testing.T) {
	l := NewLedger()
	l.Mint(1, "a", 1001) // supply 1001, quorum 40% -> ceil(400.4) = 401
	g := NewGovernor(l, NewTimelock(1, 1), 0, 10, 4000, 5000)
	if got := g.QuorumRequired(); got != 401 {
		t.Fatalf("QuorumRequired = %d, want 401 (ceiling)", got)
	}
}

func TestApprovalBoundaryExact(t *testing.T) {
	l := NewLedger()
	l.Mint(1, "a", 500)
	l.Mint(1, "b", 500)
	g := NewGovernor(l, NewTimelock(1, 1), 0, 10, 0, 5000) // approval 50%, quorum 0

	// for=500, against=500 -> ratio exactly 50% -> passes (inclusive).
	id := g.Propose(1, "a", "exact approval")
	g.CastVote(1, id, "a", true)
	g.CastVote(1, id, "b", false)
	p, _ := g.Proposal(id)
	if !g.ApprovalReached(p) {
		t.Fatal("approval ratio exactly 50% must pass (inclusive boundary)")
	}

	// for=499, against=500 -> 49.95% < 50% -> fails.
	l2 := NewLedger()
	l2.Mint(1, "a", 499)
	l2.Mint(1, "b", 500)
	g2 := NewGovernor(l2, NewTimelock(1, 1), 0, 10, 0, 5000)
	id2 := g2.Propose(1, "a", "just below")
	g2.CastVote(1, id2, "a", true)
	g2.CastVote(1, id2, "b", false)
	p2, _ := g2.Proposal(id2)
	if g2.ApprovalReached(p2) {
		t.Fatal("approval ratio 499/999 < 50% must fail")
	}
}

// --- 3. Timelock delay and execution window ---

func TestTimelockDelayAndWindowBoundaries(t *testing.T) {
	tl := NewTimelock(50, 20) // queue at 100 -> eta 150, window [150, 170)
	eta, err := tl.Queue(100, "tx1")
	if err != nil {
		t.Fatal(err)
	}
	if eta != 150 {
		t.Fatalf("eta = %d, want 150", eta)
	}

	if err := tl.Execute(149, "tx1"); err == nil {
		t.Fatal("execute before eta must fail")
	}
	if st := tl.Status(149, "tx1"); st != TxQueued {
		t.Fatalf("status at 149 = %s, want Queued", st)
	}
	if st := tl.Status(150, "tx1"); st != TxExecutable {
		t.Fatalf("status at eta = %s, want Executable", st)
	}
	if st := tl.Status(169, "tx1"); st != TxExecutable {
		t.Fatalf("status at eta+window-1 = %s, want Executable", st)
	}
	if st := tl.Status(170, "tx1"); st != TxExpired {
		t.Fatalf("status at eta+window = %s, want Expired", st)
	}

	// Boundary: execution exactly at eta succeeds.
	if err := tl.Execute(150, "tx1"); err != nil {
		t.Fatalf("execute at eta must succeed: %v", err)
	}
	if err := tl.Execute(155, "tx1"); err == nil {
		t.Fatal("double execution must fail")
	}
}

func TestTimelockLastMomentOfWindow(t *testing.T) {
	tl := NewTimelock(50, 20)
	tl.Queue(100, "tx1")
	if err := tl.Execute(169, "tx1"); err != nil {
		t.Fatalf("execute at eta+window-1 must succeed: %v", err)
	}
}

func TestTimelockExpiryRequiresRequeue(t *testing.T) {
	tl := NewTimelock(50, 20)
	tl.Queue(100, "tx1")

	// Window closes at 170; execution at 170 must fail with an explicit
	// expiry error (never a silent no-op).
	err := tl.Execute(170, "tx1")
	if err == nil || !strings.Contains(err.Error(), "requeue") {
		t.Fatalf("execute after window must fail mentioning requeue, got %v", err)
	}

	// Requeue before expiry must fail.
	tl2 := NewTimelock(50, 20)
	tl2.Queue(100, "tx")
	if _, err := tl2.Requeue(160, "tx"); err == nil {
		t.Fatal("requeue before expiry must fail")
	}

	// Requeue after expiry yields a fresh eta, then execution works.
	eta, err := tl.Requeue(200, "tx1")
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if eta != 250 {
		t.Fatalf("new eta = %d, want 250", eta)
	}
	if err := tl.Execute(260, "tx1"); err != nil {
		t.Fatalf("execute inside new window must succeed: %v", err)
	}
}

// --- 4. Cancel invalidates the queued record permanently ---

func TestCancelDuringDelayInvalidatesQueue(t *testing.T) {
	tl := NewTimelock(50, 20)
	eta, _ := tl.Queue(100, "tx1") // eta 150, window [150,170)

	if err := tl.Cancel(120, "tx1"); err != nil {
		t.Fatalf("cancel during delay: %v", err)
	}
	if st := tl.Status(160, "tx1"); st != TxCancelled {
		t.Fatalf("status inside old window = %s, want Cancelled", st)
	}
	// Even inside what would have been the execution window, the
	// cancelled record must not execute.
	if err := tl.Execute(160, "tx1"); err == nil {
		t.Fatal("cancelled tx must never execute")
	}
	// A cancelled record cannot be revived by requeue either.
	if _, err := tl.Requeue(200, "tx1"); err == nil {
		t.Fatal("cancelled tx must not be requeueable")
	}
	_ = eta
}

func TestGovernorCancelQueuedProposal(t *testing.T) {
	_, _, g := newTestEnv()
	id := g.Propose(5, "alice", "cancel me")
	g.CastVote(7, id, "alice", true)
	eta, err := g.Queue(18, id)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if eta != 38 {
		t.Fatalf("eta = %d, want 38", eta)
	}

	// A non-proposer cannot cancel.
	if err := g.Cancel(20, id, "bob"); err == nil {
		t.Fatal("non-proposer cancel must fail")
	}
	// The proposer cancels during the delay period.
	if err := g.Cancel(20, id, "alice"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// Execution inside the old window must fail.
	if err := g.Execute(40, id); err == nil {
		t.Fatal("cancelled proposal must not execute inside its old window")
	}
	st, _ := g.State(40, id)
	if st != StateCancelled {
		t.Fatalf("state = %s, want Cancelled", st)
	}
}

// --- 5. Full lifecycle through the governor ---

func TestGovernorFullLifecycle(t *testing.T) {
	_, _, g := newTestEnv()
	id := g.Propose(5, "alice", "fund the treasury")
	g.CastVote(7, id, "alice", true) // 400 for
	g.CastVote(8, id, "bob", true)   // 300 for
	g.CastVote(9, id, "carol", false)

	// Cannot queue while voting is open.
	if _, err := g.Queue(10, id); err == nil {
		t.Fatal("queue during voting must fail")
	}
	// Cannot execute before queuing.
	if err := g.Execute(16, id); err == nil {
		t.Fatal("execute before queue must fail")
	}

	eta, err := g.Queue(18, id)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if eta != 38 {
		t.Fatalf("eta = %d, want 38", eta)
	}
	if st, _ := g.State(30, id); st != StateQueued {
		t.Fatalf("state during delay = %s, want Queued", st)
	}
	if err := g.Execute(35, id); err == nil {
		t.Fatal("execute before eta must fail")
	}
	if err := g.Execute(40, id); err != nil {
		t.Fatalf("execute inside window: %v", err)
	}
	if st, _ := g.State(40, id); st != StateExecuted {
		t.Fatalf("state = %s, want Executed", st)
	}
}

func TestDefeatedProposalCannotBeQueued(t *testing.T) {
	_, _, g := newTestEnv()
	id := g.Propose(5, "alice", "unpopular")
	g.CastVote(7, id, "alice", true)  // 400 for
	g.CastVote(8, id, "bob", false)   // 300 against
	g.CastVote(9, id, "carol", false) // 200 against -> 400/900 = 44.4% < 50%
	st, err := g.Finalize(18, id)
	if err != nil || st != StateDefeated {
		t.Fatalf("finalize = %s, %v; want Defeated", st, err)
	}
	if _, err := g.Queue(18, id); err == nil {
		t.Fatal("defeated proposal must not be queued")
	}
}

func TestExpiredProposalRequeueAndExecute(t *testing.T) {
	_, _, g := newTestEnv()
	id := g.Propose(5, "alice", "slow proposal")
	g.CastVote(7, id, "alice", true)
	g.CastVote(8, id, "bob", true)
	eta, _ := g.Queue(18, id) // eta 38, window [38,48)

	if st, _ := g.State(50, id); st != StateExpired {
		t.Fatalf("state at 50 = %s, want Expired (window [38,48))", st)
	}
	if err := g.Execute(50, id); err == nil {
		t.Fatal("execute after window must fail")
	}
	newEta, err := g.Requeue(50, id)
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if newEta != 70 {
		t.Fatalf("new eta = %d, want 70", newEta)
	}
	if st, _ := g.State(60, id); st != StateQueued {
		t.Fatalf("state after requeue = %s, want Queued", st)
	}
	if err := g.Execute(75, id); err != nil {
		t.Fatalf("execute inside new window: %v", err)
	}
	_ = eta
}

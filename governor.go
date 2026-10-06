package dao

import (
	"errors"
	"fmt"
	"sort"
)

// ProposalState 提案生命周期状态。
type ProposalState string

const (
	StateActive    ProposalState = "Active"    // 投票中
	StateDefeated  ProposalState = "Defeated"  // 未通过（法定人数或赞成比例不足）
	StateSucceeded ProposalState = "Succeeded" // 已通过，待入队
	StateQueued    ProposalState = "Queued"    // 已进入时间锁延迟期
	StateExecuted  ProposalState = "Executed"  // 已在执行窗口内执行
	StateCancelled ProposalState = "Cancelled" // 已被持有者取消
)

// bps 基数：1% = 100 bps，100% = 10000 bps。
const bpsBase = uint64(10000)

// Vote 一票的记录（权重取自提案快照高度）。
type Vote struct {
	Voter   string `json:"voter"`
	Support bool   `json:"support"`
	Weight  uint64 `json:"weight"`
}

// Proposal 一个治理提案。
type Proposal struct {
	ID             uint64        `json:"id"`
	Title          string        `json:"title"`
	SnapshotHeight uint64        `json:"snapshotHeight"` // 权重快照高度，创建时固定
	StartHeight    uint64        `json:"startHeight"`
	EndHeight      uint64        `json:"endHeight"`
	ForVotes       uint64        `json:"forVotes"`
	AgainstVotes   uint64        `json:"againstVotes"`
	SupplySnapshot uint64        `json:"supplySnapshot"` // 快照高度上的总供应量（法定人数基数）
	State          ProposalState `json:"state"`
	Eta            uint64        `json:"eta"` // 入队后的最早可执行高度
	voted          map[string]bool
	votes          []Vote
}

// Votes 返回已投出的票（用于展示与回放）。
func (p *Proposal) Votes() []Vote { return p.votes }

// Event 一次状态流转记录，供回放与审计。
type Event struct {
	Height uint64 `json:"height"`
	Action string `json:"action"` // 如 propose / vote / queue / execute / cancel / requeue
	OK     bool   `json:"ok"`     // 操作是否被接受
	Detail string `json:"detail"` // 人类可读说明
}

// Governor 治理合约：提案、投票、法定人数与时间锁。
type Governor struct {
	ledger       *Ledger
	timelock     *Timelock
	votingPeriod uint64 // 投票期长度（区块数）
	quorumBps    uint64 // 法定人数：参与度下限，单位 bps（相对快照总供应量）
	thresholdBps uint64 // 通过阈值：赞成票占比下限，单位 bps（相对参与票）
	proposals    map[uint64]*Proposal
	order        []uint64
	nextID       uint64
	events       []Event
}

func NewGovernor(ledger *Ledger, timelock *Timelock, votingPeriod, quorumBps, thresholdBps uint64) *Governor {
	return &Governor{
		ledger:       ledger,
		timelock:     timelock,
		votingPeriod: votingPeriod,
		quorumBps:    quorumBps,
		thresholdBps: thresholdBps,
		proposals:    make(map[uint64]*Proposal),
		nextID:       1,
	}
}

// Events 返回已记录的事件流。
func (g *Governor) Events() []Event { return g.events }

func (g *Governor) record(height uint64, action string, ok bool, detail string) {
	g.events = append(g.events, Event{Height: height, Action: action, OK: ok, Detail: detail})
}

// Propose 在当前高度创建提案，固定该高度为权重快照。
func (g *Governor) Propose(title string, now uint64) uint64 {
	id := g.nextID
	g.nextID++
	p := &Proposal{
		ID:             id,
		Title:          title,
		SnapshotHeight: now,
		StartHeight:    now,
		EndHeight:      now + g.votingPeriod,
		SupplySnapshot: g.ledger.TotalSupplyAt(now),
		State:          StateActive,
		voted:          make(map[string]bool),
	}
	g.proposals[id] = p
	g.order = append(g.order, id)
	g.record(now, "propose", true,
		fmt.Sprintf("提案 #%d「%s」创建，快照高度 %d，快照总供应量 %d，投票期 [%d, %d]",
			id, title, p.SnapshotHeight, p.SupplySnapshot, p.StartHeight, p.EndHeight))
	return id
}

// Vote 投票。权重取自提案快照高度上的余额，之后转账不影响；
// 每个账户对每个提案只能投一次，杜绝"投票→转入→再投"的重复计权。
func (g *Governor) Vote(id uint64, voter string, support bool, now uint64) error {
	p, ok := g.proposals[id]
	if !ok {
		return fmt.Errorf("governor: 提案 %d 不存在", id)
	}
	if p.State != StateActive || now > p.EndHeight {
		err := fmt.Errorf("governor: 提案 %d 不在投票期内", id)
		g.record(now, "vote", false, fmt.Sprintf("%s 对提案 #%d 投票被拒绝: %v", voter, id, err))
		return err
	}
	if p.voted[voter] {
		err := fmt.Errorf("governor: %s 已对提案 %d 投过票，不能重复投票", voter, id)
		g.record(now, "vote", false, fmt.Sprintf("%s 对提案 #%d 重复投票被拒绝（权重固定在快照高度 %d，转账增持无效）", voter, id, p.SnapshotHeight))
		return err
	}
	weight := g.ledger.BalanceAt(voter, p.SnapshotHeight)
	if weight == 0 {
		err := fmt.Errorf("governor: %s 在快照高度 %d 上余额为 0，无投票权重", voter, p.SnapshotHeight)
		g.record(now, "vote", false, fmt.Sprintf("%s 对提案 #%d 投票被拒绝: %v", voter, id, err))
		return err
	}
	p.voted[voter] = true
	p.votes = append(p.votes, Vote{Voter: voter, Support: support, Weight: weight})
	if support {
		p.ForVotes += weight
	} else {
		p.AgainstVotes += weight
	}
	side := "反对"
	if support {
		side = "赞成"
	}
	g.record(now, "vote", true,
		fmt.Sprintf("%s 对提案 #%d 投 %s 票，权重 %d（取自快照高度 %d；当前余额 %d 不影响）",
			voter, id, side, weight, p.SnapshotHeight, g.ledger.BalanceAt(voter, now)))
	return nil
}

// QuorumReached 法定人数判定：参与权重 * 10000 >= 快照总供应量 * quorumBps。
// 恰好等于门限视为达标（闭区间）。
func (g *Governor) QuorumReached(p *Proposal) bool {
	participation := p.ForVotes + p.AgainstVotes
	return participation*bpsBase >= p.SupplySnapshot*g.quorumBps
}

// Passed 通过阈值判定：赞成票 * 10000 >= 参与票 * thresholdBps。
// 恰好等于门限视为通过（闭区间）。
func (g *Governor) Passed(p *Proposal) bool {
	participation := p.ForVotes + p.AgainstVotes
	if participation == 0 {
		return false
	}
	return p.ForVotes*bpsBase >= participation*g.thresholdBps
}

// Finalize 在投票期结束后结算提案：同时满足法定人数与赞成比例才通过。
func (g *Governor) Finalize(id, now uint64) (ProposalState, error) {
	p, ok := g.proposals[id]
	if !ok {
		return "", fmt.Errorf("governor: 提案 %d 不存在", id)
	}
	if p.State != StateActive {
		return p.State, fmt.Errorf("governor: 提案 %d 已结算（状态 %s）", id, p.State)
	}
	if now < p.EndHeight {
		return p.State, fmt.Errorf("governor: 提案 %d 投票期未结束（结束高度 %d）", id, p.EndHeight)
	}
	quorum := g.QuorumReached(p)
	passed := g.Passed(p)
	if quorum && passed {
		p.State = StateSucceeded
	} else {
		p.State = StateDefeated
	}
	g.record(now, "finalize", true,
		fmt.Sprintf("提案 #%d 结算: 参与 %d / 快照供应 %d（法定人数 %v），赞成 %d 反对 %d（通过 %v）→ %s",
			id, p.ForVotes+p.AgainstVotes, p.SupplySnapshot, quorum, p.ForVotes, p.AgainstVotes, passed, p.State))
	return p.State, nil
}

// Queue 将已通过的提案放入时间锁。
func (g *Governor) Queue(id, now uint64) error {
	p, ok := g.proposals[id]
	if !ok {
		return fmt.Errorf("governor: 提案 %d 不存在", id)
	}
	if p.State != StateSucceeded {
		err := fmt.Errorf("governor: 提案 %d 状态为 %s，不能入队", id, p.State)
		g.record(now, "queue", false, err.Error())
		return err
	}
	tx := g.timelock.Queue(id, now)
	p.State = StateQueued
	p.Eta = tx.Eta
	g.record(now, "queue", true,
		fmt.Sprintf("提案 #%d 进入时间锁: eta=%d（延迟 %d），执行窗口 [%d, %d]",
			id, tx.Eta, g.timelock.Delay, tx.Eta, tx.Eta+g.timelock.Window))
	return nil
}

// Cancel 在提案未执行前取消。若已入队，排队记录立即失效，
// 之后即使进入原执行窗口也无法执行。
func (g *Governor) Cancel(id, now uint64) error {
	p, ok := g.proposals[id]
	if !ok {
		return fmt.Errorf("governor: 提案 %d 不存在", id)
	}
	switch p.State {
	case StateActive, StateSucceeded, StateQueued:
	default:
		err := fmt.Errorf("governor: 提案 %d 状态为 %s，不能取消", id, p.State)
		g.record(now, "cancel", false, err.Error())
		return err
	}
	g.timelock.Cancel(id) // 无论是否入队都调用，保证记录失效
	p.State = StateCancelled
	g.record(now, "cancel", true,
		fmt.Sprintf("提案 #%d 被取消，原排队记录已失效，任何后续执行尝试都会失败", id))
	return nil
}

// Execute 在执行窗口内执行提案。窗口外返回明确错误（需重新排队），
// 已取消/未排队的提案返回错误（记录失效）。
func (g *Governor) Execute(id, now uint64) error {
	p, ok := g.proposals[id]
	if !ok {
		return fmt.Errorf("governor: 提案 %d 不存在", id)
	}
	if p.State != StateQueued {
		err := fmt.Errorf("governor: 提案 %d 状态为 %s，不能执行", id, p.State)
		g.record(now, "execute", false, err.Error())
		return err
	}
	if err := g.timelock.Executable(id, now); err != nil {
		g.record(now, "execute", false, fmt.Sprintf("提案 #%d 执行被拒绝: %v", id, err))
		return err
	}
	g.timelock.Dequeue(id)
	p.State = StateExecuted
	g.record(now, "execute", true,
		fmt.Sprintf("提案 #%d 在高度 %d 执行成功（窗口 [%d, %d]）", id, now, p.Eta, p.Eta+g.timelock.Window))
	return nil
}

// Requeue 窗口过期后重新排队，获得新的 eta。
func (g *Governor) Requeue(id, now uint64) error {
	p, ok := g.proposals[id]
	if !ok {
		return fmt.Errorf("governor: 提案 %d 不存在", id)
	}
	if p.State != StateQueued {
		err := fmt.Errorf("governor: 提案 %d 状态为 %s，不能重新排队", id, p.State)
		g.record(now, "requeue", false, err.Error())
		return err
	}
	tx, ok := g.timelock.Get(id)
	if !ok {
		err := fmt.Errorf("governor: 提案 %d 没有排队记录", id)
		g.record(now, "requeue", false, err.Error())
		return err
	}
	if now <= tx.Eta+g.timelock.Window {
		err := fmt.Errorf("governor: 提案 %d 执行窗口尚未过期（截止 %d），无需重新排队", id, tx.Eta+g.timelock.Window)
		g.record(now, "requeue", false, err.Error())
		return err
	}
	ntx := g.timelock.Queue(id, now)
	p.Eta = ntx.Eta
	g.record(now, "requeue", true,
		fmt.Sprintf("提案 #%d 原窗口 [%d, %d] 已过期，重新排队: 新 eta=%d，新窗口 [%d, %d]",
			id, tx.Eta, tx.Eta+g.timelock.Window, ntx.Eta, ntx.Eta, ntx.Eta+g.timelock.Window))
	return nil
}

// Proposal 返回提案。
func (g *Governor) Proposal(id uint64) (*Proposal, bool) {
	p, ok := g.proposals[id]
	return p, ok
}

// Proposals 按创建顺序返回全部提案。
func (g *Governor) Proposals() []*Proposal {
	out := make([]*Proposal, 0, len(g.order))
	for _, id := range g.order {
		out = append(out, g.proposals[id])
	}
	return out
}

// QueuedIDs 返回时间锁中当前有效的排队提案（有序，用于展示）。
func (g *Governor) QueuedIDs() []uint64 {
	ids := make([]uint64, 0)
	for _, p := range g.proposals {
		if _, ok := g.timelock.Get(p.ID); ok {
			ids = append(ids, p.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// ErrNotFound 供调用方区分"提案不存在"。
var ErrNotFound = errors.New("governor: 提案不存在")

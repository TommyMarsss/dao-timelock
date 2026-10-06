// Command replay runs a scripted set of proposal lifecycles through the
// governance engine and writes a single self-contained replay.html that
// replays every state transition step by step in the browser.
//
// Run from the repository root:
//
//	go run ./cmd/replay
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/TommyMarsss/dao-timelock/governance"
)

//go:embed template.html
var template string

type params struct {
	TotalSupply  uint64 `json:"totalSupply"`
	QuorumBps    uint64 `json:"quorumBps"`
	ApprovalBps  uint64 `json:"approvalBps"`
	VotingDelay  uint64 `json:"votingDelay"`
	VotingPeriod uint64 `json:"votingPeriod"`
	TimelockDely uint64 `json:"timelockDelay"`
	TimelockWind uint64 `json:"timelockWindow"`
	QuorumVotes  uint64 `json:"quorumVotes"`
}

type proposalView struct {
	ID        uint64 `json:"id"`
	Title     string `json:"title"`
	Proposer  string `json:"proposer"`
	State     string `json:"state"`
	For       uint64 `json:"for"`
	Against   uint64 `json:"against"`
	Snapshot  uint64 `json:"snapshot"`
	Start     uint64 `json:"start"`
	End       uint64 `json:"end"`
	Eta       uint64 `json:"eta"`
	WindowEnd uint64 `json:"windowEnd"`
	TxStatus  string `json:"txStatus"`
}

type accountView struct {
	Name    string `json:"name"`
	Balance uint64 `json:"balance"`
}

type step struct {
	Block     uint64         `json:"block"`
	Phase     string         `json:"phase"`
	Action    string         `json:"action"`
	Result    string         `json:"result"`
	OK        bool           `json:"ok"`
	Proposals []proposalView `json:"proposals"`
	Accounts  []accountView  `json:"accounts"`
}

type replayData struct {
	Params params `json:"params"`
	Steps  []step `json:"steps"`
}

type recorder struct {
	g     *governance.Governor
	l     *governance.Ledger
	tl    *governance.Timelock
	names []string
	steps []step
}

func (r *recorder) snapshot(block uint64, phase, action, result string, ok bool) {
	s := step{Block: block, Phase: phase, Action: action, Result: result, OK: ok}
	for _, p := range r.g.Proposals() {
		st, _ := r.g.State(block, p.ID)
		pv := proposalView{
			ID: p.ID, Title: p.Title, Proposer: p.Proposer,
			State: st.String(), For: p.ForVotes, Against: p.AgainstVotes,
			Snapshot: p.SnapshotBlock, Start: p.StartBlock, End: p.EndBlock,
			Eta: p.Eta, TxStatus: "-",
		}
		if p.TxID != "" {
			pv.TxStatus = r.tl.Status(block, p.TxID).String()
			pv.WindowEnd = p.Eta + r.tl.Window
		}
		s.Proposals = append(s.Proposals, pv)
	}
	for _, n := range r.names {
		s.Accounts = append(s.Accounts, accountView{Name: n, Balance: r.l.BalanceOf(n)})
	}
	r.steps = append(r.steps, s)
}

func main() {
	l := governance.NewLedger()
	tl := governance.NewTimelock(20, 10) // delay 20 blocks, execution window 10 blocks
	g := governance.NewGovernor(l, tl, 2, 10, 4000, 5000)

	r := &recorder{g: g, l: l, tl: tl, names: []string{"alice", "bob", "carol", "dave", "eve"}}

	// --- 初始化：发行代币 ---
	l.Mint(1, "alice", 400)
	l.Mint(1, "bob", 300)
	l.Mint(1, "carol", 200)
	l.Mint(1, "dave", 100)
	r.snapshot(1, "初始化",
		"发行代币：alice 400 / bob 300 / carol 200 / dave 100，总供应量 1000",
		"账本为每个账户记录余额检查点，历史区块的余额可随时回溯", true)

	// --- 创建提案（快照区块 = 5） ---
	p1 := g.Propose(5, "alice", "P1 · 金库拨款")
	p2 := g.Propose(5, "alice", "P2 · 快照攻击演示")
	p3 := g.Propose(5, "alice", "P3 · 法定人数边界")
	p4 := g.Propose(5, "alice", "P4 · 时间锁窗口演示")
	r.snapshot(5, "创建提案",
		"在区块 5 创建 4 个提案，投票权重快照固定于区块 5，投票窗口 [7, 17]",
		"此后任何余额变动都不会影响这 4 个提案的投票权重", true)

	// --- P1 投票 ---
	g.CastVote(8, p1, "alice", true)
	r.snapshot(8, "投票",
		"alice 对 P1 投赞成票，权重 = 快照区块 5 的余额 = 400",
		"投票成功，P1 赞成 400", true)
	g.CastVote(8, p1, "bob", true)
	r.snapshot(8, "投票",
		"bob 对 P1 投赞成票，权重 300",
		"投票成功，P1 赞成 700 / 反对 0", true)

	// --- P2：快照攻击尝试 ---
	g.CastVote(9, p2, "alice", true)
	r.snapshot(9, "投票",
		"alice 对 P2 投赞成票（权重 400）",
		"投票成功，P2 赞成 400", true)

	l.Mint(10, "alice", 500)
	r.snapshot(10, "快照攻击",
		"alice 在快照之后增持 500（余额 400 → 900），试图重复投票抬升权重",
		"当前余额已变为 900，但 P2 的权重仍冻结在快照区块 5", true)
	_, err := g.CastVote(11, p2, "alice", true)
	r.snapshot(11, "快照攻击",
		"alice 对 P2 再次投票",
		"被拒绝："+err.Error(), false)

	l.Mint(11, "eve", 500)
	_, err = g.CastVote(11, p2, "eve", true)
	r.snapshot(11, "快照攻击",
		"eve 在快照后获得 500 代币，试图对 P2 投票",
		"被拒绝："+err.Error(), false)

	l.Transfer(12, "bob", "carol", 300)
	w, _ := g.CastVote(12, p2, "bob", false)
	r.snapshot(12, "投票",
		fmt.Sprintf("bob 把 300 全部转给 carol 后对 P2 投反对票，权重仍取快照值 = %d", w),
		"投票成功：转账发生在快照之后，不影响权重", true)
	w, _ = g.CastVote(12, p2, "carol", false)
	r.snapshot(12, "投票",
		fmt.Sprintf("carol 当前余额 500，但快照时只有 200，对 P2 投反对票，权重 = %d", w),
		"投票成功：快照后收到的 300 不计入权重", true)

	// --- P3：法定人数边界（恰好等于门限） ---
	g.CastVote(13, p3, "alice", true)
	r.snapshot(13, "投票",
		"alice 对 P3 投赞成票（400），参与度恰好 = 法定人数门限 400",
		"边界为闭区间：参与度 >= 门限 即达成法定人数", true)

	// --- P4 投票 ---
	g.CastVote(13, p4, "alice", true)
	g.CastVote(13, p4, "bob", true)
	r.snapshot(13, "投票",
		"alice、bob 对 P4 投赞成票（400 + 300）",
		"P4 赞成 700 / 反对 0", true)

	// --- 计票 ---
	st1, _ := g.Finalize(18, p1)
	r.snapshot(18, "计票",
		"P1 投票结束：赞成 700 / 反对 0，参与度 700 ≥ 400，赞成率 100% ≥ 50%",
		"P1 → "+st1.String(), true)
	st2, _ := g.Finalize(18, p2)
	r.snapshot(18, "计票",
		"P2 投票结束：赞成 400 / 反对 500，参与度 900 ≥ 400，但赞成率 44.4% < 50%",
		"P2 → "+st2.String(), false)
	st3, _ := g.Finalize(18, p3)
	r.snapshot(18, "计票",
		"P3 投票结束：参与度恰好 400 = 门限 400（边界通过），赞成率 100%",
		"P3 → "+st3.String(), true)
	st4, _ := g.Finalize(18, p4)
	r.snapshot(18, "计票",
		"P4 投票结束：赞成 700 / 反对 0",
		"P4 → "+st4.String(), true)

	// ---  defeated 不能排队 ---
	_, err = g.Queue(18, p2)
	r.snapshot(18, "排队",
		"尝试把被否决的 P2 排入时间锁",
		"被拒绝："+err.Error(), false)

	// --- 排队 ---
	eta1, _ := g.Queue(18, p1)
	r.snapshot(18, "排队",
		fmt.Sprintf("P1 排入时间锁：eta = 18 + 20 = %d，执行窗口 [%d, %d)", eta1, eta1, eta1+10),
		"P1 → Queued，延迟期内不可执行", true)
	eta3, _ := g.Queue(18, p3)
	r.snapshot(18, "排队",
		fmt.Sprintf("P3 排入时间锁：eta = %d，执行窗口 [%d, %d)", eta3, eta3, eta3+10),
		"P3 → Queued", true)
	eta4, _ := g.Queue(18, p4)
	r.snapshot(18, "排队",
		fmt.Sprintf("P4 排入时间锁：eta = %d，执行窗口 [%d, %d)", eta4, eta4, eta4+10),
		"P4 → Queued", true)

	// --- 提前执行失败 ---
	err = g.Execute(30, p1)
	r.snapshot(30, "时间锁",
		"在 eta 之前尝试执行 P1（now=30 < eta=38）",
		"被拒绝："+err.Error(), false)

	// --- 延迟期内取消 P3 ---
	g.Cancel(32, p3, "alice")
	r.snapshot(32, "取消",
		"提案持有者 alice 在延迟期内取消 P3",
		"P3 → Cancelled，时间锁中的排队记录同步失效", true)
	err = g.Execute(40, p3)
	r.snapshot(40, "取消",
		"在 P3 原执行窗口 [38, 48) 内尝试执行已取消的 P3",
		"被拒绝："+err.Error()+"（取消的记录永远不会被执行）", false)

	// --- 窗口内执行 P1 ---
	g.Execute(40, p1)
	r.snapshot(40, "执行",
		"在执行窗口 [38, 48) 内执行 P1（now=40）",
		"P1 → Executed", true)

	// --- P4 过期 ---
	err = g.Execute(50, p4)
	r.snapshot(50, "过期",
		"P4 的执行窗口 [38, 48) 已关闭，尝试在 50 执行",
		"被拒绝："+err.Error(), false)
	newEta, _ := g.Requeue(50, p4)
	r.snapshot(50, "重新排队",
		fmt.Sprintf("P4 重新排队：新 eta = 50 + 20 = %d，新窗口 [%d, %d)", newEta, newEta, newEta+10),
		"过期不是静默丢失：必须显式重新排队才能再次执行", true)
	g.Execute(75, p4)
	r.snapshot(75, "执行",
		"在新执行窗口 [70, 80) 内执行 P4（now=75）",
		"P4 → Executed", true)

	data := replayData{
		Params: params{
			TotalSupply:  l.TotalSupply(),
			QuorumBps:    g.QuorumBps,
			ApprovalBps:  g.ApprovalBps,
			VotingDelay:  g.VotingDelay,
			VotingPeriod: g.VotingPeriod,
			TimelockDely: tl.Delay,
			TimelockWind: tl.Window,
			QuorumVotes:  g.QuorumRequired(),
		},
		Steps: r.steps,
	}
	raw, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	html := strings.Replace(template, "__DATA__", string(raw), 1)
	if err := os.WriteFile("replay.html", []byte(html), 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("wrote replay.html (%d steps)\n", len(r.steps))
}

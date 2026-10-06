// replay 运行一组提案生命周期场景，把每一步的事件与状态快照
// 内嵌进一个单一的静态 HTML 文件（report.html），
// 浏览器打开后可逐步回放提案从创建、投票、排队到取消/执行的完整状态流转。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	dao "github.com/TommyMarsss/dao-timelock"
)

// Step 回放的一步：触发事件 + 事件发生后的全局状态快照。
type Step struct {
	Height    uint64         `json:"height"`
	Action    string         `json:"action"`
	OK        bool           `json:"ok"`
	Detail    string         `json:"detail"`
	Proposals []ProposalView `json:"proposals"`
	Queue     []uint64       `json:"queue"`
}

type ProposalView struct {
	ID           uint64 `json:"id"`
	Title        string `json:"title"`
	State        string `json:"state"`
	ForVotes     uint64 `json:"forVotes"`
	AgainstVotes uint64 `json:"againstVotes"`
	Supply       uint64 `json:"supply"`
	Snapshot     uint64 `json:"snapshot"`
	Eta          uint64 `json:"eta"`
}

func snapshot(g *dao.Governor, height uint64, action string, ok bool, detail string) Step {
	s := Step{Height: height, Action: action, OK: ok, Detail: detail, Queue: g.QueuedIDs()}
	for _, p := range g.Proposals() {
		s.Proposals = append(s.Proposals, ProposalView{
			ID: p.ID, Title: p.Title, State: string(p.State),
			ForVotes: p.ForVotes, AgainstVotes: p.AgainstVotes,
			Supply: p.SupplySnapshot, Snapshot: p.SnapshotHeight, Eta: p.Eta,
		})
	}
	return s
}

// run 执行动作并把新产生的事件各自记录为一步。
func run(steps *[]Step, g *dao.Governor, fn func()) {
	before := len(g.Events())
	fn()
	for _, e := range g.Events()[before:] {
		*steps = append(*steps, snapshot(g, e.Height, e.Action, e.OK, e.Detail))
	}
}

func main() {
	ledger := dao.NewLedger()
	// 投票期 10，时间锁延迟 10，执行窗口 5，法定人数 40%，通过阈值 50%
	g := dao.NewGovernor(ledger, dao.NewTimelock(10, 5), 10, 4000, 5000)

	var steps []Step

	// 初始分配：alice 400 / bob 200 / carol 400，总供应 1000
	ledger.Mint("alice", 400, 1)
	ledger.Mint("bob", 200, 1)
	ledger.Mint("carol", 400, 1)

	// ── 提案 1：快照防护 + 正常通过并入队执行 ─────────────────────
	var p1 uint64
	run(&steps, g, func() { p1 = g.Propose("升级金库合约", 2) })
	run(&steps, g, func() { g.Vote(p1, "alice", true, 3) })
	// 投票后 carol 转 300 给 alice，alice 增持到 700，再尝试投票 → 拒绝
	ledger.Transfer("carol", "alice", 300, 4)
	run(&steps, g, func() { g.Vote(p1, "alice", true, 5) })
	run(&steps, g, func() { g.Vote(p1, "bob", true, 6) })
	run(&steps, g, func() { g.Finalize(p1, 12) })
	run(&steps, g, func() { g.Queue(p1, 12) }) // eta=22, 窗口 [22,27]
	run(&steps, g, func() { g.Execute(p1, 20) })
	run(&steps, g, func() { g.Execute(p1, 22) })

	// ── 提案 2：通过并入队，延迟期内被取消，窗口内执行失败 ────────
	var p2 uint64
	run(&steps, g, func() { p2 = g.Propose("修改手续费参数", 13) })
	run(&steps, g, func() { g.Vote(p2, "alice", true, 14) })
	run(&steps, g, func() { g.Vote(p2, "carol", true, 14) })
	run(&steps, g, func() { g.Finalize(p2, 23) })
	run(&steps, g, func() { g.Queue(p2, 23) }) // eta=33, 窗口 [33,38]
	run(&steps, g, func() { g.Cancel(p2, 28) })
	run(&steps, g, func() { g.Execute(p2, 34) })

	// ── 提案 3：窗口过期 → 重新排队 → 新窗口内执行 ────────────────
	var p3 uint64
	run(&steps, g, func() { p3 = g.Propose("新增做市商激励", 24) })
	run(&steps, g, func() { g.Vote(p3, "alice", true, 25) })
	run(&steps, g, func() { g.Vote(p3, "carol", true, 25) })
	run(&steps, g, func() { g.Finalize(p3, 34) })
	run(&steps, g, func() { g.Queue(p3, 34) }) // eta=44, 窗口 [44,49]
	run(&steps, g, func() { g.Execute(p3, 50) })
	run(&steps, g, func() { g.Requeue(p3, 50) }) // 新 eta=60, 窗口 [60,65]
	run(&steps, g, func() { g.Execute(p3, 61) })

	// ── 提案 4：双边界 —— 参与恰好 400/1000 = 40%，赞成恰好 50% → 通过
	// 先转 100 给 dave，使投票权重可凑出恰好 400 的参与度
	ledger.Transfer("alice", "dave", 100, 30)
	var p4 uint64
	run(&steps, g, func() { p4 = g.Propose("边界提案：恰好达到法定人数与阈值", 35) })
	run(&steps, g, func() { g.Vote(p4, "carol", true, 36) }) // carol 100 赞成
	run(&steps, g, func() { g.Vote(p4, "dave", true, 36) })  // dave 100 赞成
	run(&steps, g, func() { g.Vote(p4, "bob", false, 36) })  // bob 200 反对
	// 参与 400 = 恰好 40% 法定人数；赞成 200/400 = 恰好 50% 阈值 → 通过
	run(&steps, g, func() { g.Finalize(p4, 45) })

	data, err := json.Marshal(steps)
	if err != nil {
		fmt.Fprintln(os.Stderr, "序列化失败:", err)
		os.Exit(1)
	}
	html := strings.Replace(pageTemplate, "__DATA__", string(data), 1)
	if err := os.WriteFile("report.html", []byte(html), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写入 report.html 失败:", err)
		os.Exit(1)
	}
	fmt.Printf("已生成 report.html（%d 个回放步骤）\n", len(steps))
}

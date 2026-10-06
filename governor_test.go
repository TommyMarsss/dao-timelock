package dao

import (
	"strings"
	"testing"
)

// 测试公共配置：投票期 10，时间锁延迟 10，执行窗口 5，
// 法定人数 40%（4000 bps），通过阈值 50%（5000 bps）。
func newTestGovernor() (*Ledger, *Governor) {
	ledger := NewLedger()
	tl := NewTimelock(10, 5)
	g := NewGovernor(ledger, tl, 10, 4000, 5000)
	return ledger, g
}

// 快照机制：投票后转账增持，再次投票被拒绝；结算权重仍取快照值。
func TestSnapshotWeightUnaffectedByLaterTransfers(t *testing.T) {
	ledger, g := newTestGovernor()
	// 高度 1：alice 400, bob 600，总供应 1000
	ledger.Mint("alice", 400, 1)
	ledger.Mint("bob", 600, 1)

	// 高度 2：创建提案，快照高度 = 2
	id := g.Propose("增持攻击测试", 2)

	// 高度 3：alice 投赞成票，权重应为快照值 400
	if err := g.Vote(id, "alice", true, 3); err != nil {
		t.Fatalf("首次投票失败: %v", err)
	}

	// 高度 4：bob 转 500 给 alice，alice 当前余额变为 900
	if err := ledger.Transfer("bob", "alice", 500, 4); err != nil {
		t.Fatalf("转账失败: %v", err)
	}
	if got := ledger.BalanceAt("alice", 4); got != 900 {
		t.Fatalf("转账后余额应为 900，实际 %d", got)
	}
	// 但快照高度 2 上 alice 仍是 400
	if got := ledger.BalanceAt("alice", 2); got != 400 {
		t.Fatalf("快照高度余额应为 400，实际 %d", got)
	}

	// 高度 5：alice 用增持后的余额再次投票 —— 必须被拒绝
	if err := g.Vote(id, "alice", true, 5); err == nil {
		t.Fatal("重复投票应被拒绝")
	}

	// 结算：赞成票只能是快照权重 400，而不是 900 或 1300
	if _, err := g.Finalize(id, 12); err != nil {
		t.Fatalf("结算失败: %v", err)
	}
	p, _ := g.Proposal(id)
	if p.ForVotes != 400 {
		t.Fatalf("赞成票应为快照权重 400，实际 %d", p.ForVotes)
	}
	if p.ForVotes+p.AgainstVotes != 400 {
		t.Fatalf("总票权不应被重复计算，实际 %d", p.ForVotes+p.AgainstVotes)
	}
}

// 快照机制：快照高度之后才有余额的账户没有投票权重。
func TestSnapshotZeroBalanceCannotVote(t *testing.T) {
	ledger, g := newTestGovernor()
	ledger.Mint("alice", 1000, 1)
	id := g.Propose("零余额快照", 2)
	// carol 在快照之后才获得代币
	if err := ledger.Transfer("alice", "carol", 500, 3); err != nil {
		t.Fatalf("转账失败: %v", err)
	}
	if err := g.Vote(id, "carol", true, 4); err == nil {
		t.Fatal("快照高度上余额为 0 的账户不应有投票权重")
	}
}

// 法定人数边界：参与度恰好等于门限视为达标，差 1 则不达标。
func TestQuorumBoundary(t *testing.T) {
	// 总供应 1000，法定人数 40% → 需要参与票 >= 400
	cases := []struct {
		name          string
		forWeight     uint64
		expectQuorum  bool
		expectSuccess bool
	}{
		{"恰好达到法定人数", 400, true, true},
		{"差一票不达法定人数", 399, false, false},
		{"超过法定人数", 401, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledger, g := newTestGovernor()
			ledger.Mint("alice", tc.forWeight, 1)
			ledger.Mint("bob", 1000-tc.forWeight, 1)
			id := g.Propose("法定人数边界", 2)
			if err := g.Vote(id, "alice", true, 3); err != nil {
				t.Fatalf("投票失败: %v", err)
			}
			if _, err := g.Finalize(id, 12); err != nil {
				t.Fatalf("结算失败: %v", err)
			}
			p, _ := g.Proposal(id)
			if got := g.QuorumReached(p); got != tc.expectQuorum {
				t.Fatalf("法定人数判定应为 %v，实际 %v", tc.expectQuorum, got)
			}
			wantState := StateDefeated
			if tc.expectSuccess {
				wantState = StateSucceeded
			}
			if p.State != wantState {
				t.Fatalf("状态应为 %s，实际 %s", wantState, p.State)
			}
		})
	}
}

// 通过阈值边界：赞成比例恰好等于门限视为通过，差一票则失败。
func TestThresholdBoundary(t *testing.T) {
	// 总供应 1000，法定人数 40%，通过阈值 50%
	// alice 投 200 赞成 + bob 投 200 反对 → 参与 400 达法定人数，赞成恰好 50% → 通过
	ledger, g := newTestGovernor()
	ledger.Mint("alice", 200, 1)
	ledger.Mint("bob", 200, 1)
	ledger.Mint("carol", 600, 1)
	id := g.Propose("阈值边界-恰好通过", 2)
	g.Vote(id, "alice", true, 3)
	g.Vote(id, "bob", false, 3)
	g.Finalize(id, 12)
	p, _ := g.Proposal(id)
	if !g.Passed(p) || p.State != StateSucceeded {
		t.Fatalf("赞成比例恰好 50%% 应通过，状态 %s", p.State)
	}

	// alice 199 赞成 + bob 201 反对 → 赞成 < 50% → 失败
	ledger2, g2 := newTestGovernor()
	ledger2.Mint("alice", 199, 1)
	ledger2.Mint("bob", 201, 1)
	ledger2.Mint("carol", 600, 1)
	id2 := g2.Propose("阈值边界-差一票", 2)
	g2.Vote(id2, "alice", true, 3)
	g2.Vote(id2, "bob", false, 3)
	g2.Finalize(id2, 12)
	p2, _ := g2.Proposal(id2)
	if g2.Passed(p2) || p2.State != StateDefeated {
		t.Fatalf("赞成比例不足 50%% 应失败，状态 %s", p2.State)
	}
}

// 时间锁：延迟期内不能执行，窗口内可执行，窗口外执行报明确错误且需重新排队。
func TestTimelockDelayAndWindow(t *testing.T) {
	ledger, g := newTestGovernor()
	ledger.Mint("alice", 1000, 1)
	id := g.Propose("时间锁窗口", 2)
	g.Vote(id, "alice", true, 3)
	g.Finalize(id, 12)
	if err := g.Queue(id, 12); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	// eta = 12 + 10 = 22，窗口 [22, 27]

	// 延迟期内执行 → 拒绝
	if err := g.Execute(id, 21); err == nil {
		t.Fatal("延迟期内执行应被拒绝")
	} else if !strings.Contains(err.Error(), "延迟未满") {
		t.Fatalf("错误应说明延迟未满，实际: %v", err)
	}

	// 窗口起点（边界）→ 可执行
	ledger2, g2 := newTestGovernor()
	ledger2.Mint("alice", 1000, 1)
	id2 := g2.Propose("窗口起点", 2)
	g2.Vote(id2, "alice", true, 3)
	g2.Finalize(id2, 12)
	g2.Queue(id2, 12)
	if err := g2.Execute(id2, 22); err != nil {
		t.Fatalf("窗口起点执行应成功: %v", err)
	}

	// 窗口终点（边界）→ 可执行
	ledger3, g3 := newTestGovernor()
	ledger3.Mint("alice", 1000, 1)
	id3 := g3.Propose("窗口终点", 2)
	g3.Vote(id3, "alice", true, 3)
	g3.Finalize(id3, 12)
	g3.Queue(id3, 12)
	if err := g3.Execute(id3, 27); err != nil {
		t.Fatalf("窗口终点执行应成功: %v", err)
	}

	// 窗口外（28）→ 明确报错，需要重新排队
	if err := g.Execute(id, 28); err == nil {
		t.Fatal("窗口外执行应被拒绝")
	} else if !strings.Contains(err.Error(), "重新排队") {
		t.Fatalf("错误应提示重新排队，实际: %v", err)
	}
	// 状态保持 Queued，不会静默过期
	p, _ := g.Proposal(id)
	if p.State != StateQueued {
		t.Fatalf("过期后状态应保持 Queued，实际 %s", p.State)
	}

	// 重新排队 → 新 eta = 28 + 10 = 38，窗口 [38, 43]
	if err := g.Requeue(id, 28); err != nil {
		t.Fatalf("重新排队失败: %v", err)
	}
	if err := g.Execute(id, 37); err == nil {
		t.Fatal("新延迟期内执行应被拒绝")
	}
	if err := g.Execute(id, 38); err != nil {
		t.Fatalf("重新排队后窗口内执行应成功: %v", err)
	}
	p, _ = g.Proposal(id)
	if p.State != StateExecuted {
		t.Fatalf("执行后状态应为 Executed，实际 %s", p.State)
	}
	// 重复执行 → 拒绝
	if err := g.Execute(id, 39); err == nil {
		t.Fatal("重复执行应被拒绝")
	}
}

// 取消：延迟期内取消后，原排队记录失效，即使进入原执行窗口也不能执行。
func TestCancelInvalidatesQueuedTx(t *testing.T) {
	ledger, g := newTestGovernor()
	ledger.Mint("alice", 1000, 1)
	id := g.Propose("取消测试", 2)
	g.Vote(id, "alice", true, 3)
	g.Finalize(id, 12)
	g.Queue(id, 12) // eta=22, 窗口 [22,27]

	// 延迟期内取消
	if err := g.Cancel(id, 15); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	p, _ := g.Proposal(id)
	if p.State != StateCancelled {
		t.Fatalf("状态应为 Cancelled，实际 %s", p.State)
	}

	// 原窗口内尝试执行 → 必须失败
	if err := g.Execute(id, 22); err == nil {
		t.Fatal("取消后在原执行窗口内执行应被拒绝")
	}
	if err := g.Execute(id, 25); err == nil {
		t.Fatal("取消后在原执行窗口内执行应被拒绝")
	}

	// 取消后不能重新排队、不能再次入队
	if err := g.Requeue(id, 30); err == nil {
		t.Fatal("已取消提案不应能重新排队")
	}
	if err := g.Queue(id, 30); err == nil {
		t.Fatal("已取消提案不应能再次入队")
	}
}

// 未通过 / 投票中的提案不能入队。
func TestOnlySucceededCanQueue(t *testing.T) {
	ledger, g := newTestGovernor()
	ledger.Mint("alice", 100, 1) // 10% 参与，不达 40% 法定人数
	ledger.Mint("bob", 900, 1)
	id := g.Propose("未通过提案", 2)
	g.Vote(id, "alice", true, 3)
	g.Finalize(id, 12)
	if err := g.Queue(id, 12); err == nil {
		t.Fatal("Defeated 提案不应能入队")
	}
}

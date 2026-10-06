# dao-timelock

用 Go 标准库实现的链上治理核心机制：**权重快照投票 + 法定人数判定 + 时间锁延迟执行**。
不依赖任何第三方库；附带的回放页面为纯原生 HTML/CSS/JavaScript 单文件。

## 结构

| 文件 | 说明 |
|---|---|
| `token.go` | 支持历史快照查询的代币账本（checkpoint 机制） |
| `governor.go` | 提案、投票、法定人数/通过阈值判定、生命周期状态机 |
| `timelock.go` | 时间锁：延迟排队、有限执行窗口、取消失效、重新排队 |
| `governor_test.go` | 自动化测试（快照、边界值、窗口、取消） |
| `cmd/replay/` | 场景回放生成器，输出单文件 `report.html` |

## 一、权重快照模型

账本为每个账户（及总供应量）维护一条按区块高度单调递增的 **checkpoint 序列**：
每次余额变动追加一条 `(height, balance)` 记录，`BalanceAt(addr, h)` 二分查找高度 `≤ h`
的最后一个 checkpoint，从而可以查询任意历史高度上的余额。

提案在创建时把当前高度固定为 `SnapshotHeight`，同时记录该高度的总供应量
`SupplySnapshot`。投票时：

```
weight = BalanceAt(voter, proposal.SnapshotHeight)   // 与投票时的当前余额无关
```

并且每个账户对每个提案只能投一次（`voted` 集合）。因此"投票 → 转入代币增持 →
再次投票"的攻击无效：第二次投票被拒绝，第一次投票的权重也永远取快照高度的值。
快照高度之后余额才从 0 变为正的账户，在该提案中权重为 0，不能投票。

## 二、法定人数与通过阈值

参数以基点（bps）表示，1% = 100 bps，避免浮点误差。设：

- `S` = 快照高度总供应量（`SupplySnapshot`）
- `P` = 参与权重 = 赞成票 + 反对票
- `F` = 赞成票

判定（均为**闭区间**，恰好等于门限视为达标）：

```
法定人数:  P × 10000 ≥ S × quorumBps        （默认 quorumBps = 4000，即 40%）
通过阈值:  F × 10000 ≥ P × thresholdBps     （默认 thresholdBps = 5000，即 50%）
```

两个条件**同时满足**提案才进入 `Succeeded`，否则为 `Defeated`。
乘法在比较左侧进行（`P × 10000` 而非 `P / S`），整数运算无精度损失；
`uint64` 下供应量需 < 2⁶⁴/10000，对现实代币供应量足够。

## 三、时间锁状态机

```
                 Finalize                 Queue
  Active ──────────────────► Succeeded ──────────► Queued ──Execute(窗口内)──► Executed
     │                          │                     │
     │                          │                     │ Execute(窗口外) → 明确报错
     │                          │                     │   · now < eta        → "延迟未满"
     │                          │                     │   · now > eta+window → "窗口已过，需重新排队"
     │                          │                     │ Requeue(窗口过期后) → 新 eta = now + delay
     └────────── Cancel ────────┴─────────────────────┘
                    ▼
               Cancelled（排队记录删除，永久不可执行）
```

- **入队**：`eta = now + delay`，执行窗口为闭区间 `[eta, eta + window]`。
- **执行**：仅当 `eta ≤ now ≤ eta + window` 且排队记录存在时允许；成功后记录移除，
  防止重复执行。窗口外执行返回明确错误，**不会静默失败**，提案保持 `Queued` 状态。
- **重新排队**：仅当窗口已过期（`now > eta + window`）时允许，获得新的 `eta`；
  窗口未过期时拒绝（防止任意推迟）。
- **取消**：`Active` / `Succeeded` / `Queued` 状态下持有者可取消。取消会**删除
  时间锁中的排队记录**，此后即使进入原执行窗口，`Execute` 也必然失败——
  记录失效，而非仅仅标记状态。

## 运行

```sh
go test ./...        # 运行测试
go run ./cmd/replay  # 生成 report.html（28 步回放）
open report.html     # 浏览器中逐步回放（←/→ 方向键也可翻页）
```

## 测试覆盖

- `TestSnapshotWeightUnaffectedByLaterTransfers` — 投票后转账增持，重复投票被拒绝，结算权重仍取快照值
- `TestSnapshotZeroBalanceCannotVote` — 快照后才有余额的账户无投票权重
- `TestQuorumBoundary` — 参与度恰好等于 / 差 1 / 超过法定人数门限
- `TestThresholdBoundary` — 赞成比例恰好 50% 通过、不足 50% 失败
- `TestTimelockDelayAndWindow` — 延迟期内拒绝、窗口起点/终点边界可执行、窗口外报错并重新排队后执行
- `TestCancelInvalidatesQueuedTx` — 取消后原窗口内执行失败，且不能再入队/重新排队
- `TestOnlySucceededCanQueue` — 未通过提案不能进入时间锁

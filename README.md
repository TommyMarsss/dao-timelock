# dao-timelock

链上治理的投票与时间锁执行模拟：权重快照、法定人数 / 通过阈值判定、时间锁排队 /
取消 / 执行窗口。仅使用 Go 标准库；回放页面为单一静态 HTML（原生 HTML/CSS/JS，
无任何框架或第三方库）。

## 运行

```sh
go test ./...        # 运行全部测试
go run ./cmd/replay  # 在仓库根目录生成 replay.html
open replay.html     # 浏览器中逐步回放 4 个提案的完整生命周期
```

## 一、权重快照模型

`Ledger` 为每个账户维护一条按区块高度递增的**余额检查点**序列
`(block, balance)`。同一区块内的多次写入只保留最后一次。

- 提案创建时把当前区块固定为 `SnapshotBlock`，之后不再改变。
- 投票权重 = `GetPriorVotes(voter, SnapshotBlock)`，即快照区块上最后一个
  检查点的余额（二分查找）。
- 快照之后的增发、转账只影响当前余额，不影响任何已存在提案的权重。
- 每个账户对每个提案只能投一次；快照时权重为 0 的账户不能投票。

由此保证："投票后转账增加余额再投票"既不能重复计票，也不能提升权重；
快照后收到代币的账户在该提案中权重仍为旧值（见
`TestSnapshotIgnoresPostSnapshotBalanceChanges`）。

## 二、法定人数与通过阈值

全部使用整数运算（`math/bits` 做 128 位中间计算），无浮点误差：

```
quorumRequired  = ceil(totalSupply × QuorumBps / 10000)
participation   = forVotes + againstVotes
quorumReached   = participation ≥ quorumRequired          （边界含等号）
approvalReached = forVotes × 10000 ≥ ApprovalBps × participation   （边界含等号）
succeeded       = quorumReached && approvalReached
```

- 法定人数向上取整：供应量 1001、门限 40% → 需要 401 票。
- 边界为**闭区间**：参与度恰好等于门限即达成；赞成率恰好等于阈值即通过。
- 两个条件必须同时满足，投票期结束后 `Finalize` 判定 `Succeeded` / `Defeated`。

## 三、时间锁状态机

```
                 Queue(now)
                    │  eta = now + Delay
                    ▼
        ┌───────────────────────┐   now ≥ eta+Window    ┌──────────┐
        │  Queued（延迟期内）    │ ────────────────────▶ │ Expired  │
        └───────────────────────┘                       └──────────┘
          │  now ∈ [eta, eta+Window)          ▲            │ Requeue
          ▼                                    │            │ eta = now + Delay
        ┌──────────────┐   Execute   ┌─────────┴───┐ ◀──────┘
        │ Executable   │ ──────────▶ │  Executed   │（终态）
        └──────────────┘             └─────────────┘
          │ Cancel（执行前任意时刻）
          ▼
        ┌─────────────┐
        │  Cancelled  │（终态：不可执行、不可重新排队）
        └─────────────┘
```

规则：

- **延迟期**：`now < eta`，不可执行；持有者可取消。
- **执行窗口**：`[eta, eta+Window)`，左闭右开——`eta` 时刻可执行，
  `eta+Window` 时刻已过期。
- **过期**：窗口关闭后执行会返回明确错误（提示需要 requeue），不会静默
  失败；`Requeue` 赋予新的 eta 后可在新窗口内执行。
- **取消**：取消是终态，原排队记录永久失效，即使在原执行窗口内也绝不
  会被执行，也不能被 requeue 复活。
- 所有非法操作（未排队、重复执行、提前执行、过期执行、取消后执行）都
  返回带原因的错误。

## 四、目录结构

```
governance/ledger.go      余额检查点账本（快照模型）
governance/governor.go    提案、投票、法定人数/阈值判定、与时间锁的衔接
governance/timelock.go    时间锁状态机（排队/取消/执行/过期/重排）
governance/governance_test.go  快照、边界值、窗口、取消的自动化测试
cmd/replay/main.go        运行一组提案生命周期并生成 replay.html
cmd/replay/template.html  回放页面模板（原生 HTML/CSS/JS）
replay.html               生成的单文件回放页（数据与逻辑全部内嵌）
```

## 五、测试覆盖

| 测试 | 验证点 |
| --- | --- |
| `TestSnapshotIgnoresPostSnapshotBalanceChanges` | 快照后增持/转账不影响权重；重复投票被拒 |
| `TestGetPriorVotesBoundaries` | 检查点查询的区块边界 |
| `TestQuorumBoundaryExact` | 参与度恰好等于门限（通过）与低于门限（否决） |
| `TestQuorumRoundsUp` | 法定人数向上取整 |
| `TestApprovalBoundaryExact` | 赞成率恰好等于阈值（通过）与略低于阈值（否决） |
| `TestTimelockDelayAndWindowBoundaries` | eta 前不可执行、eta 时刻可执行、窗口右端过期 |
| `TestTimelockLastMomentOfWindow` | 窗口最后一刻可执行 |
| `TestTimelockExpiryRequiresRequeue` | 过期执行报错、重排后可在新窗口执行 |
| `TestCancelDuringDelayInvalidatesQueue` | 取消后原窗口内不可执行、不可重排 |
| `TestGovernorCancelQueuedProposal` | 非持有者不能取消；取消后执行被拒 |
| `TestGovernorFullLifecycle` | 创建→投票→排队→执行全流程 |
| `TestDefeatedProposalCannotBeQueued` | 被否决提案不能进入时间锁 |
| `TestExpiredProposalRequeueAndExecute` | 提案级过期→重排→执行 |

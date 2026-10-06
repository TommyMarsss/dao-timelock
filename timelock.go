package dao

import "fmt"

// QueuedTx 是时间锁中的一条排队记录。
// Eta 为最早可执行高度（入队高度 + 延迟），
// 可执行窗口为闭区间 [Eta, Eta+Window]。
type QueuedTx struct {
	ProposalID uint64
	Eta        uint64
}

// Timelock 管理提案通过后的延迟排队与有限执行窗口。
//
// 状态语义：
//   - 入队：eta = now + Delay，记录生效；
//   - 执行：仅当 Eta <= now <= Eta+Window 且记录存在时允许；
//   - 取消：删除记录，此后任何执行尝试都会失败（记录失效）；
//   - 过期：now > Eta+Window 时执行会返回明确错误（不会静默失败），
//     必须重新排队获得新的 eta。
type Timelock struct {
	Delay  uint64 // 延迟期（区块数）
	Window uint64 // 执行窗口长度（区块数）
	txs    map[uint64]QueuedTx
}

func NewTimelock(delay, window uint64) *Timelock {
	return &Timelock{Delay: delay, Window: window, txs: make(map[uint64]QueuedTx)}
}

// Queue 将提案放入时间锁，返回其 eta。
func (t *Timelock) Queue(proposalID, now uint64) QueuedTx {
	tx := QueuedTx{ProposalID: proposalID, Eta: now + t.Delay}
	t.txs[proposalID] = tx
	return tx
}

// Cancel 使提案的排队记录失效。记录被删除，之后 Execute 必然失败。
func (t *Timelock) Cancel(proposalID uint64) {
	delete(t.txs, proposalID)
}

// Get 返回提案的排队记录及是否存在。
func (t *Timelock) Get(proposalID uint64) (QueuedTx, bool) {
	tx, ok := t.txs[proposalID]
	return tx, ok
}

// Executable 检查提案当前是否处于可执行窗口内。
// 返回 nil 表示可执行；否则返回明确的错误原因：
// 未排队（或已被取消）、延迟未满、或窗口已过需要重新排队。
func (t *Timelock) Executable(proposalID, now uint64) error {
	tx, ok := t.txs[proposalID]
	if !ok {
		return fmt.Errorf("timelock: 提案 %d 没有有效的排队记录（未排队或已取消）", proposalID)
	}
	if now < tx.Eta {
		return fmt.Errorf("timelock: 提案 %d 延迟未满: 当前高度 %d, 最早可执行高度 %d", proposalID, now, tx.Eta)
	}
	if now > tx.Eta+t.Window {
		return fmt.Errorf("timelock: 提案 %d 执行窗口已过: 当前高度 %d, 窗口截止高度 %d, 需要重新排队", proposalID, now, tx.Eta+t.Window)
	}
	return nil
}

// Dequeue 在执行成功后移除记录，防止重复执行。
func (t *Timelock) Dequeue(proposalID uint64) {
	delete(t.txs, proposalID)
}

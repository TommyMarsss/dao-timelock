package dao

import "fmt"

// checkpoint 记录某一区块高度上某个账户（或总供应量）的余额。
// 每个账户的 checkpoint 列表按高度单调不减排列。
type checkpoint struct {
	height  uint64
	balance uint64
}

// Ledger 是一个支持历史快照查询的代币账本。
// 所有余额变动都会追加一条 checkpoint，因此可以查询任意历史高度上的余额，
// 这正是投票权重快照机制的基础：提案创建时固定一个快照高度，
// 之后发生的转账不会改变该高度上的历史余额。
type Ledger struct {
	balances map[string][]checkpoint
	supply   []checkpoint
}

// NewLedger 创建一个空账本。
func NewLedger() *Ledger {
	return &Ledger{balances: make(map[string][]checkpoint)}
}

// Mint 在指定高度铸造代币（增加账户余额与总供应量）。
func (l *Ledger) Mint(to string, amount, height uint64) {
	l.setBalance(to, l.BalanceAt(to, height)+amount, height)
	l.setSupply(l.TotalSupplyAt(height)+amount, height)
}

// Transfer 在指定高度转账。总供应量不变。
func (l *Ledger) Transfer(from, to string, amount, height uint64) error {
	bal := l.BalanceAt(from, height)
	if bal < amount {
		return fmt.Errorf("ledger: %s 余额不足: 有 %d, 需要 %d", from, bal, amount)
	}
	l.setBalance(from, bal-amount, height)
	l.setBalance(to, l.BalanceAt(to, height)+amount, height)
	return nil
}

// BalanceAt 返回账户在指定高度（含）之前最近一次 checkpoint 的余额。
func (l *Ledger) BalanceAt(addr string, height uint64) uint64 {
	return atOrBefore(l.balances[addr], height)
}

// TotalSupplyAt 返回指定高度（含）之前的总供应量。
func (l *Ledger) TotalSupplyAt(height uint64) uint64 {
	return atOrBefore(l.supply, height)
}

// setBalance 追加（或在同高度覆盖）一条账户 checkpoint。
func (l *Ledger) setBalance(addr string, balance, height uint64) {
	l.balances[addr] = appendCheckpoint(l.balances[addr], checkpoint{height, balance})
}

func (l *Ledger) setSupply(supply, height uint64) {
	l.supply = appendCheckpoint(l.supply, checkpoint{height, supply})
}

// appendCheckpoint 保持列表按高度有序：同高度覆盖，否则追加。
// 调用方必须保证高度单调不减（链上时间天然满足）。
func appendCheckpoint(cps []checkpoint, cp checkpoint) []checkpoint {
	if n := len(cps); n > 0 {
		if cps[n-1].height == cp.height {
			cps[n-1] = cp
			return cps
		}
		if cps[n-1].height > cp.height {
			panic("ledger: checkpoint 高度必须单调不减")
		}
	}
	return append(cps, cp)
}

// atOrBefore 二分查找高度 <= height 的最后一个 checkpoint。
func atOrBefore(cps []checkpoint, height uint64) uint64 {
	lo, hi := 0, len(cps)
	for lo < hi {
		mid := (lo + hi) / 2
		if cps[mid].height <= height {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return 0
	}
	return cps[lo-1].balance
}

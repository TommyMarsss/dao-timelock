package governance

import "fmt"

// Checkpoint records an account's balance at a specific block height.
// Checkpoints per account are stored in non-decreasing block order.
type Checkpoint struct {
	Block   uint64
	Balance uint64
}

// Ledger is a minimal token ledger that keeps per-account balance
// checkpoints, so the historical voting weight of any account can be
// reconstructed for any past block height. This is the foundation of the
// voting-weight snapshot model: a proposal pins a snapshot block at
// creation, and all votes on it are weighted by balances as of that
// block, no matter how balances move afterwards.
type Ledger struct {
	balances    map[string]uint64
	checkpoints map[string][]Checkpoint
	totalSupply uint64
}

func NewLedger() *Ledger {
	return &Ledger{
		balances:    make(map[string]uint64),
		checkpoints: make(map[string][]Checkpoint),
	}
}

// TotalSupply returns the current total token supply.
func (l *Ledger) TotalSupply() uint64 { return l.totalSupply }

// BalanceOf returns the current (head) balance of an account.
func (l *Ledger) BalanceOf(account string) uint64 { return l.balances[account] }

// Mint creates new tokens for an account, checkpointing at the given block.
func (l *Ledger) Mint(block uint64, to string, amount uint64) {
	l.balances[to] += amount
	l.totalSupply += amount
	l.writeCheckpoint(to, block, l.balances[to])
}

// Transfer moves tokens between accounts, checkpointing both sides at the
// given block. Blocks are expected to be non-decreasing across calls.
func (l *Ledger) Transfer(block uint64, from, to string, amount uint64) error {
	if l.balances[from] < amount {
		return fmt.Errorf("ledger: insufficient balance: %s has %d, needs %d", from, l.balances[from], amount)
	}
	l.balances[from] -= amount
	l.balances[to] += amount
	l.writeCheckpoint(from, block, l.balances[from])
	l.writeCheckpoint(to, block, l.balances[to])
	return nil
}

func (l *Ledger) writeCheckpoint(account string, block, balance uint64) {
	cps := l.checkpoints[account]
	if n := len(cps); n > 0 && cps[n-1].Block == block {
		// Multiple writes in the same block: keep the latest balance.
		cps[n-1].Balance = balance
		return
	}
	l.checkpoints[account] = append(cps, Checkpoint{Block: block, Balance: balance})
}

// GetPriorVotes returns the account's balance as of the end of the given
// block: the balance recorded by the latest checkpoint whose block is
// <= the requested block. Balance changes after that block never affect
// the result, which is exactly what the snapshot voting model requires.
func (l *Ledger) GetPriorVotes(account string, block uint64) uint64 {
	cps := l.checkpoints[account]
	// Binary search for the last checkpoint with Block <= block.
	lo, hi := 0, len(cps)
	for lo < hi {
		mid := (lo + hi) / 2
		if cps[mid].Block <= block {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return 0
	}
	return cps[lo-1].Balance
}

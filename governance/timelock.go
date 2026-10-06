package governance

import "fmt"

// TxStatus describes the lifecycle state of a timelocked transaction.
// TxExecutable and TxExpired are derived from the clock; the others are
// persisted transitions.
type TxStatus int

const (
	TxUnknown TxStatus = iota
	TxQueued
	TxExecutable
	TxExecuted
	TxCancelled
	TxExpired
)

func (s TxStatus) String() string {
	switch s {
	case TxQueued:
		return "Queued"
	case TxExecutable:
		return "Executable"
	case TxExecuted:
		return "Executed"
	case TxCancelled:
		return "Cancelled"
	case TxExpired:
		return "Expired"
	default:
		return "Unknown"
	}
}

type queuedTx struct {
	eta   uint64
	state TxStatus // one of TxQueued, TxExecuted, TxCancelled
}

// Timelock enforces a delay between queuing and execution, plus a bounded
// execution window after the delay elapses:
//
//	queue at t        -> eta = t + Delay
//	executable during -> [eta, eta+Window)
//	expired at        -> eta+Window (must be requeued; never silently dropped)
//
// A cancelled transaction is terminally invalid: it can never be executed,
// even inside what would have been its execution window.
type Timelock struct {
	Delay  uint64
	Window uint64
	txs    map[string]*queuedTx
}

func NewTimelock(delay, window uint64) *Timelock {
	return &Timelock{Delay: delay, Window: window, txs: make(map[string]*queuedTx)}
}

// Queue registers a transaction and returns its eta (earliest execution
// time). Re-queueing an id that is still queued is an error.
func (t *Timelock) Queue(now uint64, id string) (uint64, error) {
	if tx, ok := t.txs[id]; ok && tx.state == TxQueued {
		return 0, fmt.Errorf("timelock: tx %q is already queued (eta=%d)", id, tx.eta)
	}
	eta := now + t.Delay
	t.txs[id] = &queuedTx{eta: eta, state: TxQueued}
	return eta, nil
}

// Cancel invalidates a queued transaction. It is allowed at any time
// before execution (during the delay period or the execution window).
// Afterwards the record is terminally Cancelled: Execute on it always
// fails, so a stale queue entry can never fire inside a later window.
func (t *Timelock) Cancel(now uint64, id string) error {
	tx, ok := t.txs[id]
	if !ok {
		return fmt.Errorf("timelock: tx %q was never queued", id)
	}
	if tx.state != TxQueued {
		return fmt.Errorf("timelock: tx %q is %s, cannot cancel", id, tx.state)
	}
	tx.state = TxCancelled
	return nil
}

// Execute runs a queued transaction, but only inside its execution window
// [eta, eta+Window). Every failure mode is reported explicitly: unknown id,
// cancelled, already executed, too early, or expired (requeue required).
func (t *Timelock) Execute(now uint64, id string) error {
	tx, ok := t.txs[id]
	if !ok {
		return fmt.Errorf("timelock: tx %q was never queued", id)
	}
	switch tx.state {
	case TxCancelled:
		return fmt.Errorf("timelock: tx %q was cancelled and can never be executed", id)
	case TxExecuted:
		return fmt.Errorf("timelock: tx %q was already executed", id)
	}
	if now < tx.eta {
		return fmt.Errorf("timelock: tx %q is not ready: eta=%d, now=%d", id, tx.eta, now)
	}
	if now >= tx.eta+t.Window {
		return fmt.Errorf("timelock: tx %q expired: window closed at %d, now=%d; requeue required", id, tx.eta+t.Window, now)
	}
	tx.state = TxExecuted
	return nil
}

// Requeue gives an expired transaction a fresh eta. Only a still-queued
// transaction whose window has fully closed may be requeued; a cancelled
// or executed transaction can never come back.
func (t *Timelock) Requeue(now uint64, id string) (uint64, error) {
	tx, ok := t.txs[id]
	if !ok {
		return 0, fmt.Errorf("timelock: tx %q was never queued", id)
	}
	if tx.state != TxQueued {
		return 0, fmt.Errorf("timelock: tx %q is %s, cannot requeue", id, tx.state)
	}
	if now < tx.eta+t.Window {
		return 0, fmt.Errorf("timelock: tx %q has not expired yet (window closes at %d, now=%d)", id, tx.eta+t.Window, now)
	}
	tx.eta = now + t.Delay
	return tx.eta, nil
}

// Status derives the current status of a transaction at the given time.
func (t *Timelock) Status(now uint64, id string) TxStatus {
	tx, ok := t.txs[id]
	if !ok {
		return TxUnknown
	}
	if tx.state != TxQueued {
		return tx.state
	}
	switch {
	case now < tx.eta:
		return TxQueued
	case now < tx.eta+t.Window:
		return TxExecutable
	default:
		return TxExpired
	}
}

// Eta returns the current eta of a transaction, if known.
func (t *Timelock) Eta(id string) (uint64, bool) {
	tx, ok := t.txs[id]
	if !ok {
		return 0, false
	}
	return tx.eta, true
}

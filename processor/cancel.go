// SPDX-License-Identifier: MIT

package processor

import "sync"

// scanCancellation is the stop signal for one run. The channel is created when
// the run starts and closed at most once, so every stage can select on the same
// signal and repeated cancellation requests are harmless.
type scanCancellation struct {
	done chan struct{}
	once sync.Once
}

func newScanCancellation() *scanCancellation {
	return &scanCancellation{done: make(chan struct{})}
}

// signal returns the channel stages select on. A nil receiver yields nil,
// which blocks forever in a select and so means "never canceled" — the shape
// every processorContext built without a run gets.
func (c *scanCancellation) signal() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.done
}

// cancel closes the signal exactly once.
func (c *scanCancellation) cancel() {
	if c == nil {
		return
	}
	c.once.Do(func() { close(c.done) })
}

// canceled reports whether the run has already been asked to stop.
func (c *scanCancellation) canceled() bool {
	if c == nil {
		return false
	}
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// Only one scan runs at a time in a process (the MCP server serializes calls
// explicitly), so a single package-level slot is enough. Registering the run
// here is what lets CancelScan reach it without adding a parameter to Process
// or ProcessResult, whose public signatures must not change.
var (
	cancelMu     sync.Mutex
	activeCancel *scanCancellation
)

// beginScanCancellation registers a fresh stop signal for a run and returns it
// together with a cleanup that unregisters exactly this signal. The cleanup
// makes a cancellation that lands after the run finished a no-op for the next
// run, which matters for long-lived processes running scans back to back.
func beginScanCancellation() (*scanCancellation, func()) {
	c := newScanCancellation()

	cancelMu.Lock()
	activeCancel = c
	cancelMu.Unlock()

	return c, func() {
		cancelMu.Lock()
		if activeCancel == c {
			activeCancel = nil
		}
		cancelMu.Unlock()
	}
}

// CancelScan asks the running scan to stop dispatching new files and unwind as
// soon as it can. The walker is terminated, the stat and count workers stop
// pulling from their queues, and every channel is closed by the same closer
// goroutines that close them on a normal run, so no goroutine is left behind.
// Output already produced is still summarized once. It is a no-op when no scan
// is running and safe to call any number of times.
//
// This is the cancellation entry point for library users; the Process and
// ProcessResult signatures are deliberately left unchanged.
func CancelScan() {
	cancelMu.Lock()
	c := activeCancel
	cancelMu.Unlock()
	c.cancel()
}

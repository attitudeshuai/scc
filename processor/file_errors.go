// SPDX-License-Identifier: MIT

package processor

import (
	"slices"
	"strings"
	"sync"
)

// FileError records one file the scan could not process and the reason. A bad
// file never takes a worker down with it: the worker records the error here
// and carries on with the next job, and the errors are reported separately
// from the counted results once the run has finished.
type FileError struct {
	// Location is the path the worker was asked to process.
	Location string
	// Op names the stage that failed: "stat" while turning a walked path
	// into a job or "read" while loading its contents.
	Op string
	// Err is the underlying operating system error.
	Err error
}

// fileErrorCollector is the run-wide sink every worker appends to. All access
// takes the mutex because stat and read workers run in separate pools.
type fileErrorCollector struct {
	mu     sync.Mutex
	errors []FileError
}

func (c *fileErrorCollector) reset() {
	c.mu.Lock()
	c.errors = nil
	c.mu.Unlock()
}

func (c *fileErrorCollector) add(location, op string, err error) {
	if err == nil {
		return
	}

	c.mu.Lock()
	c.errors = append(c.errors, FileError{Location: location, Op: op, Err: err})
	c.mu.Unlock()
}

// snapshot returns the run's errors in canonical path order, so the report is
// deterministic in the same way the counted output is. The order workers met
// the files in must not influence it.
func (c *fileErrorCollector) snapshot() []FileError {
	c.mu.Lock()
	errors := slices.Clone(c.errors)
	c.mu.Unlock()

	slices.SortFunc(errors, func(a, b FileError) int {
		if order := compareNormalizedPath(a.Location, b.Location); order != 0 {
			return order
		}
		// Same path failing at more than one stage: keep a fixed stage
		// order so the pair's order is total.
		return strings.Compare(a.Op, b.Op)
	})

	return errors
}

// scanFileErrors holds the errors of the run in progress. It is reset at the
// start of every pipeline entry point so a long-lived process (the MCP
// server) never reports one run's bad files in the next run.
var scanFileErrors = &fileErrorCollector{}

// resetFileErrors clears errors left by a previous run. Called alongside the
// other per-run resets (cleanVisitedPaths, cleanDuplicates).
func resetFileErrors() {
	scanFileErrors.reset()
}

// FileErrors returns a snapshot of the files the current run could not stat
// or read, sorted by normalized path. The scan itself completes successfully;
// the counted output simply omits these files and the errors are reported
// separately. The returned slice is a copy safe to keep after the run.
func FileErrors() []FileError {
	return scanFileErrors.snapshot()
}

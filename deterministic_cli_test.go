// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeCLIDeterministicTree creates a nested tree whose files are named out of
// order and repeated across directories, so any worker-dependent emission
// order would show up as a diff. The CLI tests run the real binary and diff
// its full output between worker layouts.
func makeCLIDeterministicTree(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	dirs := []string{"one", "two", filepath.Join("one", "nested"), filepath.Join("two", "nested", "deep")}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
			t.Fatal(err)
		}
	}

	names := []string{"zebra.go", "apple.go", "mango.go", "shared.go"}
	for di, d := range dirs {
		for ni, name := range names {
			body := "package cli\n"
			for line := 0; line < (di+1)*(ni+1)+1; line++ {
				body += "// comment line\n"
			}
			if err := os.WriteFile(filepath.Join(dir, d, name), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}

	return dir
}

// workerArgs returns the pool configuration for one layout. The flags already
// existed; no new concurrency parameters are introduced by this work.
func workerArgs(workers string) []string {
	return []string{
		"--file-process-job-workers", workers,
		"--file-list-job-workers", workers,
		"--directory-walker-job-workers", workers,
		"--file-list-queue-size", workers,
		"--file-summary-job-queue-size", workers,
	}
}

// TestCLIDeterministicAcrossWorkerCounts is the command-line form of the
// acceptance check: every output format must be byte-identical between a
// single-worker run and a four-worker run.
func TestCLIDeterministicAcrossWorkerCounts(t *testing.T) {
	t.Parallel()

	dir := makeCLIDeterministicTree(t)

	formats := [][]string{
		{"-f", "tabular"},
		{"-f", "tabular", "--by-file"},
		{"-f", "wide", "--by-file"},
		{"-f", "json", "--by-file"},
		{"-f", "json2", "--by-file"},
		{"-f", "csv", "--by-file"},
		{"-f", "csv-stream"},
		{"-f", "openmetrics", "--by-file"},
		{"-f", "html", "--by-file"},
	}

	for _, formatArgs := range formats {
		t.Run(formatArgs[1], func(t *testing.T) {
			one := append(append([]string{}, formatArgs...), workerArgs("1")...)
			four := append(append([]string{}, formatArgs...), workerArgs("4")...)

			outOne, errOne := runSCCDir(t, dir, noGlobalConfig, one...)
			if errOne != nil {
				t.Fatalf("single-worker run failed: %v\n%s", errOne, outOne)
			}
			outFour, errFour := runSCCDir(t, dir, noGlobalConfig, four...)
			if errFour != nil {
				t.Fatalf("four-worker run failed: %v\n%s", errFour, outFour)
			}

			if outOne != outFour {
				t.Fatalf("output differs between worker layouts\n--- worker=1 ---\n%s\n--- worker=4 ---\n%s", outOne, outFour)
			}
		})
	}
}

// TestCLIBadFileDoesNotBreakScan checks the end-to-end behavior of an
// unreadable file: the process exits successfully, every other file is still
// counted, the error is reported separately, and worker count changes neither
// the counted output nor the error report.
func TestCLIBadFileDoesNotBreakScan(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions; cannot simulate an unreadable file")
	}

	dir := makeCLIDeterministicTree(t)
	bad := filepath.Join(dir, "one", "shared.go")
	if err := os.Chmod(bad, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0644) })

	base := []string{"-f", "csv", "--by-file"}

	outOne, errOne := runSCCDir(t, dir, noGlobalConfig, append(base, workerArgs("1")...)...)
	if errOne != nil {
		t.Fatalf("single-worker run should succeed with a bad file: %v\n%s", errOne, outOne)
	}
	outFour, errFour := runSCCDir(t, dir, noGlobalConfig, append(base, workerArgs("4")...)...)
	if errFour != nil {
		t.Fatalf("four-worker run should succeed with a bad file: %v\n%s", errFour, outFour)
	}

	if outOne != outFour {
		t.Fatalf("output with a bad file differs between worker layouts\n--- worker=1 ---\n%s\n--- worker=4 ---\n%s", outOne, outFour)
	}

	for _, out := range []string{outOne, outFour} {
		if !strings.Contains(out, "could not be processed and were not counted") {
			t.Errorf("expected a separate bad-file report, got:\n%s", out)
		}
		if !strings.Contains(out, "shared.go") {
			t.Errorf("expected the bad file path in the report, got:\n%s", out)
		}
		if !strings.Contains(out, "zebra.go") {
			t.Errorf("expected the good files to still be counted, got:\n%s", out)
		}
	}
}

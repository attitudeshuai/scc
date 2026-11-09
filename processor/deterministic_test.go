// SPDX-License-Identifier: MIT

package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/boyter/gocodewalker"
)

// workerLayout is one pool configuration the determinism tests run the same
// scan under. The two layouts are deliberately extreme: single workers with
// one-deep queues serialise every handover, while four workers with deeper
// queues interleave aggressively. Byte-identical output under both is the
// acceptance bar.
type workerLayout struct {
	process int
	list    int
	walker  int
	qSize   int
}

var (
	serialLayout   = workerLayout{process: 1, list: 1, walker: 1, qSize: 1}
	parallelLayout = workerLayout{process: 4, list: 4, walker: 8, qSize: 4}
)

// makeDeterministicTree lays out a tree big and varied enough that worker
// scheduling actually has something to reorder: nested directories, names
// created out of alphabetical order, the same basename in several directories
// (exercising the path tie-break) and some files with identical stats.
func makeDeterministicTree(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	dirs := []string{"alpha", "beta", "gamma", filepath.Join("alpha", "sub"), filepath.Join("beta", "deep", "nest")}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
			t.Fatal(err)
		}
	}

	names := []string{"zeta.go", "mu.go", "alpha.go", "iota.go", "eta.go", "same.go", "common.go", "last.go"}
	for di, d := range dirs {
		for ni, name := range names {
			content := fmt.Sprintf("package d%d\n", di)
			// Distinct line counts per file, with a repeated pair so some rows
			// tie on every numeric sort key and only the path separates them.
			lines := (di+1)*(ni+1) + 1
			if ni == len(names)-1 {
				lines = 7 // "last.go" is identical-sized in every directory
			}
			for i := 0; i < lines; i++ {
				content += fmt.Sprintf("// directory %d file %s line %d\n", di, name, i)
			}
			if err := os.WriteFile(filepath.Join(dir, d, name), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}

	return dir
}

// savedGlobals captures every package-level knob the determinism tests touch
// and restores them on cleanup, so repeated in-process runs start from the
// same state the rest of the suite expects.
type savedGlobals struct {
	process, list, walker, listQ, summaryQ int
	dirPaths                               []string
	format, sortBy                         string
	files, more, duplicates, verbose       bool
	deny, allow, exclude                   []string
}

func saveGlobals() savedGlobals {
	return savedGlobals{
		process:    FileProcessJobWorkers,
		list:       FileListJobWorkers,
		walker:     DirectoryWalkerJobWorkers,
		listQ:      FileListQueueSize,
		summaryQ:   FileSummaryJobQueueSize,
		dirPaths:   append([]string(nil), DirFilePaths...),
		format:     Format,
		sortBy:     SortBy,
		files:      Files,
		more:       More,
		duplicates: Duplicates,
		verbose:    Verbose,
		deny:       append([]string(nil), PathDenyList...),
		allow:      append([]string(nil), AllowListExtensions...),
		exclude:    append([]string(nil), ExcludeListExtensions...),
	}
}

func (s savedGlobals) restore() {
	FileProcessJobWorkers = s.process
	FileListJobWorkers = s.list
	DirectoryWalkerJobWorkers = s.walker
	FileListQueueSize = s.listQ
	FileSummaryJobQueueSize = s.summaryQ
	DirFilePaths = s.dirPaths
	Format = s.format
	SortBy = s.sortBy
	Files = s.files
	More = s.more
	Duplicates = s.duplicates
	Verbose = s.verbose
	PathDenyList = s.deny
	AllowListExtensions = s.allow
	ExcludeListExtensions = s.exclude
}

// configureScan pins the run-wide globals for one deterministic scan. The
// queues are deliberately small so workers spend real time blocked on each
// other, which maximises interleaving in the parallel layout.
func configureScan(t *testing.T, dir string, layout workerLayout) {
	t.Helper()

	ProcessConstants()
	cleanVisitedPaths()
	resetFileErrors()

	FileProcessJobWorkers = layout.process
	FileListJobWorkers = layout.list
	DirectoryWalkerJobWorkers = layout.walker
	FileListQueueSize = layout.qSize
	FileSummaryJobQueueSize = layout.qSize
	DirFilePaths = []string{dir}
	PathDenyList = nil
	AllowListExtensions = nil
	ExcludeListExtensions = nil
	Verbose = false
	Duplicates = false
}

// runProcessCaptured drives the full Process pipeline and returns everything
// it wrote to stdout. stderr is left alone so the separate bad-file report
// never pollutes the byte comparison.
func runProcessCaptured(t *testing.T) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	Process()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestNormalizedPathOrdering pins the canonical ordering every per-file output
// uses, including the Clean and separator normalisation that makes it platform
// independent.
func TestNormalizedPathOrdering(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"a/z.go", "a/a.go", 1},
		{"./a/../b/x.go", "b/x.go", 0},
		{"a/sub.go", "a/sub.go", 0},
		{"a", "b", -1},
	}

	for _, tc := range cases {
		if got := compareNormalizedPath(tc.a, tc.b); got != tc.want {
			t.Errorf("compareNormalizedPath(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}

	if got := normalizedPath(filepath.Join("dir", "sub", "..", "f.go")); got != "dir/f.go" {
		t.Errorf("normalizedPath = %q, want dir/f.go", got)
	}
}

// TestFileErrorsCollectedInPathOrder verifies the collector is concurrency
// safe and its snapshot has the same canonical path order as the counted
// output, with the stage name breaking ties for one path failing twice.
func TestFileErrorsCollectedInPathOrder(t *testing.T) {
	collector := &fileErrorCollector{}
	collector.add("b.go", "read", errors.New("denied"))
	collector.add("a.go", "stat", errors.New("gone"))
	collector.add("a.go", "read", errors.New("denied"))
	collector.add("c.go", "read", nil) // nil errors must be ignored

	got := collector.snapshot()
	if len(got) != 3 {
		t.Fatalf("expected 3 errors, got %d: %#v", len(got), got)
	}

	want := []struct {
		location, op string
	}{
		{"a.go", "read"},
		{"a.go", "stat"},
		{"b.go", "read"},
	}
	for i, w := range want {
		if got[i].Location != w.location || got[i].Op != w.op {
			t.Errorf("error %d = (%s, %s), want (%s, %s)", i, got[i].Location, got[i].Op, w.location, w.op)
		}
		if got[i].Err == nil {
			t.Errorf("error %d lost its underlying error", i)
		}
	}
}

// TestDeterministicOutputAcrossWorkerCounts is the core byte-identity
// acceptance test: every per-file and summary format must print identical
// bytes under the serial and parallel layouts, repeated to catch scheduling
// luck rather than ordering.
func TestDeterministicOutputAcrossWorkerCounts(t *testing.T) {
	saved := saveGlobals()
	defer saved.restore()

	dir := makeDeterministicTree(t)

	formats := []struct {
		name  string
		setup func()
		// filter lets timestamp-bearing formats compare only their stable
		// records; their wall-clock metadata is inherently run dependent.
		filter func(string) string
	}{
		{name: "tabular-summary", setup: func() { Format = "tabular"; Files = false; More = false }},
		{name: "tabular-by-file", setup: func() { Format = "tabular"; Files = true; More = false }},
		{name: "wide-by-file", setup: func() { Format = "wide"; Files = true; More = true }},
		{name: "json-by-file", setup: func() { Format = "json"; Files = true; More = false }},
		{name: "json2-by-file", setup: func() { Format = "json2"; Files = true; More = false }},
		{name: "csv-summary", setup: func() { Format = "csv"; Files = false; More = false }},
		{name: "csv-by-file", setup: func() { Format = "csv"; Files = true; More = false }},
		{name: "csv-stream", setup: func() { Format = "csv-stream"; Files = false; More = false }},
		{name: "openmetrics-by-file", setup: func() { Format = "openmetrics"; Files = true; More = false }},
		{name: "html-by-file", setup: func() { Format = "html"; Files = true; More = false }},
		{
			name:  "sql-insert-records",
			setup: func() { Format = "sql-insert"; Files = false; More = false },
			filter: func(out string) string {
				var lines []string
				for _, line := range strings.Split(out, "\n") {
					if strings.Contains(line, "insert into t values") {
						lines = append(lines, line)
					}
				}
				return strings.Join(lines, "\n")
			},
		},
	}

	for _, fc := range formats {
		t.Run(fc.name, func(t *testing.T) {
			var outputs []string

			for run := 0; run < 2; run++ {
				for _, layout := range []workerLayout{serialLayout, parallelLayout} {
					configureScan(t, dir, layout)
					fc.setup()
					out := runProcessCaptured(t)
					if fc.filter != nil {
						out = fc.filter(out)
					}
					outputs = append(outputs, out)
				}
			}

			base := outputs[0]
			if base == "" {
				t.Fatal("scan produced no output")
			}
			for i, out := range outputs[1:] {
				if out != base {
					t.Fatalf("output %d differs from the serial reference output\n--- reference ---\n%s\n--- got ---\n%s", i+1, base, out)
				}
			}
		})
	}
}

// TestDeterministicStatsAcrossWorkerCounts compares the structured
// ProcessResult results field by field, including the ordered per-file
// records, so statistics cannot drift with the worker count.
func TestDeterministicStatsAcrossWorkerCounts(t *testing.T) {
	saved := saveGlobals()
	defer saved.restore()

	dir := makeDeterministicTree(t)

	run := func(layout workerLayout) []LanguageSummary {
		configureScan(t, dir, layout)
		Format = "json"
		Files = true
		More = false
		SortBy = ""

		summary, err := ProcessResult()
		if err != nil {
			t.Fatalf("ProcessResult: %s", err)
		}
		return summary
	}

	serial := run(serialLayout)
	parallel := run(parallelLayout)

	if len(serial) != len(parallel) {
		t.Fatalf("language count differs: serial %d, parallel %d", len(serial), len(parallel))
	}

	var totalFilesSerial, totalFilesParallel int64
	for i := range serial {
		a, b := serial[i], parallel[i]
		if a.Name != b.Name {
			t.Fatalf("language %d name differs: %q vs %q", i, a.Name, b.Name)
		}
		if a.Count != b.Count || a.Lines != b.Lines || a.Code != b.Code || a.Comment != b.Comment ||
			a.Blank != b.Blank || a.Complexity != b.Complexity || a.Cognitive != b.Cognitive || a.Bytes != b.Bytes {
			t.Fatalf("language %q stats differ:\nserial=%+v\nparallel=%+v", a.Name, a, b)
		}
		totalFilesSerial += a.Count
		totalFilesParallel += b.Count

		if len(a.Files) != len(b.Files) {
			t.Fatalf("language %q file count differs: %d vs %d", a.Name, len(a.Files), len(b.Files))
		}
		for j := range a.Files {
			if a.Files[j].Location != b.Files[j].Location {
				t.Fatalf("language %q file %d order differs: %q vs %q", a.Name, j, a.Files[j].Location, b.Files[j].Location)
			}
			fa, fb := a.Files[j], b.Files[j]
			if fa.Lines != fb.Lines || fa.Code != fb.Code || fa.Comment != fb.Comment || fa.Blank != fb.Blank ||
				fa.Complexity != fb.Complexity || fa.Bytes != fb.Bytes {
				t.Fatalf("file %q stats differ between layouts", fa.Location)
			}
		}
	}

	if totalFilesSerial != totalFilesParallel || totalFilesSerial == 0 {
		t.Fatalf("total file count mismatch or empty: %d vs %d", totalFilesSerial, totalFilesParallel)
	}

	// The retained files must themselves be in canonical path order.
	for _, summary := range serial {
		for j := 1; j < len(summary.Files); j++ {
			if compareNormalizedPath(summary.Files[j-1].Location, summary.Files[j].Location) > 0 {
				t.Fatalf("files of %q are not in normalized path order at %d", summary.Name, j)
			}
		}
	}
}

// TestDuplicateElectionDeterministicAcrossWorkers runs --no-duplicates style
// dedupe under both layouts, with per-file output, and requires the surviving
// paths (not just the counts) to be byte-identical. The summary-stage filter
// elects the smallest normalized path, so worker finish order cannot decide
// which duplicate survives.
func TestDuplicateElectionDeterministicAcrossWorkers(t *testing.T) {
	saved := saveGlobals()
	defer saved.restore()

	// A tree whose duplicate set spans several directories: the same content
	// under different paths, with unique files alongside them.
	dir := t.TempDir()
	dirs := []string{"dup-a", "dup-b", filepath.Join("dup-b", "nested"), "unique"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	dupContent := []byte("package shared\n// identical bytes in every copy\n")
	for _, d := range dirs[:3] {
		if err := os.WriteFile(filepath.Join(dir, d, "copy.go"), dupContent, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "unique", "only.go"), []byte("package only\n// one of a kind\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var outputs []string
	for run := 0; run < 2; run++ {
		for _, layout := range []workerLayout{serialLayout, parallelLayout} {
			configureScan(t, dir, layout)
			Duplicates = true
			Format = "csv"
			Files = true
			outputs = append(outputs, runProcessCaptured(t))
		}
	}

	base := outputs[0]
	for i, out := range outputs[1:] {
		if out != base {
			t.Fatalf("duplicate output %d differs from the serial reference\n--- reference ---\n%s\n--- got ---\n%s", i+1, base, out)
		}
	}

	// Exactly one member of the duplicate set survives, and it is the member
	// with the smallest normalized path rather than a scheduling favorite.
	if !strings.Contains(base, filepath.Join("dup-a", "copy.go")) {
		t.Fatalf("expected the smallest-path duplicate to survive, got:\n%s", base)
	}
	if strings.Contains(base, filepath.Join("dup-b", "copy.go")) {
		t.Fatalf("a losing duplicate was printed, output:\n%s", base)
	}
	if !strings.Contains(base, filepath.Join("unique", "only.go")) {
		t.Fatalf("the unique file must always be counted, output:\n%s", base)
	}
}

// TestBadFileReportedSeparately makes one file unreadable and checks that the
// worker survives it, the other files are still counted, the error is
// collected once in canonical order and the counted output stays identical
// across worker counts.
func TestBadFileReportedSeparately(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions; cannot simulate an unreadable file")
	}

	saved := saveGlobals()
	defer saved.restore()

	dir := makeDeterministicTree(t)
	bad := filepath.Join(dir, "alpha", "same.go")
	if err := os.Chmod(bad, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0644) })

	run := func(layout workerLayout) (string, []LanguageSummary, []FileError) {
		configureScan(t, dir, layout)
		Format = "csv"
		Files = true
		summary, err := ProcessResult()
		if err != nil {
			t.Fatalf("ProcessResult must not fail on a bad file: %s", err)
		}
		fileErrs := FileErrors()

		// Run the same scan again through Process so the formatted output is
		// compared byte for byte; the bad file stays unreadable, so reset the
		// per-run collectors before re-running.
		configureScan(t, dir, layout)
		Format = "csv"
		Files = true
		captured := runProcessCaptured(t)
		return captured, summary, fileErrs
	}

	outSerial, summarySerial, errsSerial := run(serialLayout)
	outParallel, summaryParallel, errsParallel := run(parallelLayout)

	if outSerial != outParallel {
		t.Fatalf("counted output differs because of the bad file\nserial:\n%s\nparallel:\n%s", outSerial, outParallel)
	}

	for _, errs := range [][]FileError{errsSerial, errsParallel} {
		if len(errs) != 1 {
			t.Fatalf("expected exactly one collected error, got %d: %#v", len(errs), errs)
		}
		if errs[0].Location != bad || errs[0].Op != "read" || errs[0].Err == nil {
			t.Fatalf("bad file error not recorded correctly: %#v", errs[0])
		}
	}

	var countSerial, countParallel int64
	for _, s := range summarySerial {
		countSerial += s.Count
	}
	for _, s := range summaryParallel {
		countParallel += s.Count
	}
	if countSerial == 0 || countSerial != countParallel {
		t.Fatalf("good files must still be counted equally: %d vs %d", countSerial, countParallel)
	}
	if !strings.Contains(outSerial, "good") && !strings.Contains(outSerial, ".go") {
		t.Fatal("expected the surviving files to be printed")
	}
}

// TestCancelStopsProducer is the unit-level cancellation check for the stat
// pool: a pre-canceled run must unwind and close its output queue instead of
// dispatching anything, without hanging.
func TestCancelStopsProducer(t *testing.T) {
	dir := makeDeterministicTree(t)

	cancel := newScanCancellation()
	cancel.cancel()

	potential := make(chan []*gocodewalker.File, 1)
	potential <- []*gocodewalker.File{{Location: filepath.Join(dir, "alpha", "zeta.go"), Filename: "zeta.go"}}
	close(potential)

	fileListQueue := make(chan *FileJob, 10)
	startFileJobProducer(potential, nil, nil, fileListQueue, cancel)

	select {
	case _, ok := <-fileListQueue:
		if ok {
			t.Fatal("a canceled producer must not dispatch jobs")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled producer never closed its output queue")
	}
}

// TestCancelStopsProcessorWorker checks the counting pool closes its output
// channel when canceled even with jobs still queued, while a nil cancel keeps
// the original blocking behavior.
func TestCancelStopsProcessorWorker(t *testing.T) {
	dir := t.TempDir()
	jobFor := func(name string) *FileJob {
		location := filepath.Join(dir, name)
		if err := os.WriteFile(location, []byte("package x\n"), 0644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(location)
		if err != nil {
			t.Fatal(err)
		}
		return &FileJob{
			Filename:  name,
			Location:  location,
			Extension: "go",
			Bytes:     info.Size(),
		}
	}

	cancel := newScanCancellation()
	cancel.cancel()

	input := make(chan *FileJob, 5)
	for i := 0; i < 5; i++ {
		input <- jobFor(fmt.Sprintf("canceled%02d.go", i))
	}
	close(input)

	output := make(chan *FileJob, 5)
	ctx := processorContext{remap: newRemapConfig("", ""), cancel: cancel}
	ctx.fileProcessorWorker(input, output)

	// Some already-counted jobs may be buffered, but the channel must reach
	// its close promptly; drain and require the close.
	deadline := time.After(2 * time.Second)
	closed := false
	for !closed {
		select {
		case _, ok := <-output:
			if !ok {
				closed = true
			}
		case <-deadline:
			t.Fatal("canceled worker pool never closed its output channel")
		}
	}

	// A second, uncanceled run on the same style of pipeline still completes,
	// proving cancellation did not poison shared state.
	open := make(chan *FileJob, 1)
	open <- jobFor("survives.go")
	close(open)
	normalOut := make(chan *FileJob, 1)
	normalCtx := processorContext{remap: newRemapConfig("", "")}
	normalCtx.fileProcessorWorker(open, normalOut)
	job, ok := <-normalOut
	if !ok || job == nil {
		t.Fatal("uncanceled worker stopped counting after a canceled run")
	}
	if job.Location != filepath.Join(dir, "survives.go") {
		t.Fatalf("uncanceled worker counted the wrong job: %q", job.Location)
	}
	if _, ok := <-normalOut; ok {
		t.Fatal("uncanceled worker did not close its output channel")
	}
}

// TestCancelScanEndToEnd runs a large scan, cancels it after it has started,
// and asserts the run unwinds, the walker and every pool clean up, and a
// following scan runs to completion with the full counts.
func TestCancelScanEndToEnd(t *testing.T) {
	saved := saveGlobals()
	defer saved.restore()

	dir := t.TempDir()
	const dirCount, filesPerDir = 25, 60
	for d := 0; d < dirCount; d++ {
		sub := filepath.Join(dir, fmt.Sprintf("dir%02d", d))
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
		for f := 0; f < filesPerDir; f++ {
			path := filepath.Join(sub, fmt.Sprintf("file%03d.go", f))
			content := "package x\n" + strings.Repeat("// line\n", (d+f)%20+1)
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	const totalFiles = dirCount * filesPerDir

	configureScan(t, dir, parallelLayout)
	Format = "json"
	Files = true

	baselineGoroutines := runtime.NumGoroutine()

	go func() {
		// Give the scan a moment to start walking, then cancel. The queues are
		// only four deep, so a tree this size cannot finish in the window.
		time.Sleep(2 * time.Millisecond)
		CancelScan()
	}()

	done := make(chan error, 1)
	var partial []LanguageSummary
	go func() {
		s, err := ProcessResult()
		partial = s
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected error from canceled scan: %s", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("canceled scan did not unwind within 30s")
	}

	// Every worker goroutine must go away once the canceled run has returned.
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > baselineGoroutines+2 && time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
	if leaked := runtime.NumGoroutine() - baselineGoroutines; leaked > 2 {
		t.Fatalf("cancellation leaked %d goroutines", leaked)
	}

	// Canceling again now that the run has unregistered must be a harmless
	// no-op rather than a panic on a closed channel.
	CancelScan()
	CancelScan()

	// The next scan is fully usable and counts the whole tree, proving the
	// cancellation state was cleaned up rather than carried into the next run.
	configureScan(t, dir, serialLayout)
	Format = "json"
	Files = true
	full, err := ProcessResult()
	if err != nil {
		t.Fatalf("scan after cancellation failed: %s", err)
	}
	var counted int64
	for _, summary := range full {
		counted += summary.Count
	}
	if counted != totalFiles {
		t.Fatalf("post-cancel scan counted %d files, want %d", counted, totalFiles)
	}

	// The canceled run never over-counted: partial results, when any came
	// back, are a subset of the tree rather than duplicated work.
	var partialCount int64
	for _, summary := range partial {
		partialCount += summary.Count
	}
	if partialCount > totalFiles {
		t.Fatalf("canceled run counted %d files, more than the %d in the tree", partialCount, totalFiles)
	}
}

// TestCancelScanWhenIdle guards the no-active-run path: calling CancelScan
// before any scan has registered must not panic.
func TestCancelScanWhenIdle(t *testing.T) {
	CancelScan()
	CancelScan()
}

// TestCancelProcessPrintPath exercises cancellation through Process itself,
// including the default tabular mode where the workers fold the run into
// shared totals: the canceled run must still print once, close every stage and
// leave the shared summary consumed rather than merged twice.
func TestCancelProcessPrintPath(t *testing.T) {
	saved := saveGlobals()
	defer saved.restore()

	dir := t.TempDir()
	for d := 0; d < 15; d++ {
		sub := filepath.Join(dir, fmt.Sprintf("d%02d", d))
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
		for f := 0; f < 40; f++ {
			path := filepath.Join(sub, fmt.Sprintf("f%03d.go", f))
			if err := os.WriteFile(path, []byte(strings.Repeat("// x\n", (d+f)%15+1)), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}

	configureScan(t, dir, parallelLayout)
	Format = "tabular"
	Files = false
	More = false

	go func() {
		time.Sleep(2 * time.Millisecond)
		CancelScan()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runProcessCaptured(t)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Process did not return after cancellation")
	}

	// The worker-folded summary must have been consumed exactly once; a
	// leftover would be merged into the next unrelated scan.
	if workerSummary != nil {
		t.Fatal("worker summary was not consumed after the canceled run")
	}
}

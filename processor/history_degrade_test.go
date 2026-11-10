// SPDX-License-Identifier: MIT

// Every history analysis must survive the three missing-history environments
// without an error or a panic:
//
//  1. a path that is not a git repository at all,
//  2. a shallow clone whose walk reaches the shallow boundary,
//  3. a repository with a single commit.
//
// Missing data degrades to an empty window carrying one uniform notice,
// rendered by every format, and must never disturb the working-tree
// statistics that do not depend on history. On the unmodified code the
// no-git case exits with "repository does not exist" and the shallow case
// has no annotation, so these tests are red before the fix.

package processor

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeNoGitDir returns a temp directory holding ordinary source files but
// no .git directory anywhere above it (t.TempDir is per-test, so nothing in
// the fixture tree can resolve to a repository).
func makeNoGitDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// historyHarness binds one report's observer constructor to its renderer.
type historyHarness struct {
	report      string
	newObserver func() CommitObserver
	render      func(CommitObserver) (string, error)
}

func historyHarnesses() []historyHarness {
	return []historyHarness{
		{
			report:      "hotspots",
			newObserver: func() CommitObserver { return newHotspotsObserver() },
			render: func(o CommitObserver) (string, error) {
				return renderHotspots(o.(*hotspotsObserver))
			},
		},
		{
			report:      "coupling",
			newObserver: func() CommitObserver { return newCouplingObserver() },
			render: func(o CommitObserver) (string, error) {
				return renderCoupling(o.(*couplingObserver))
			},
		},
		{
			report:      "authors",
			newObserver: func() CommitObserver { return newHistoryAuthorsObserver() },
			render: func(o CommitObserver) (string, error) {
				return renderAuthors(o.(*historyAuthorsObserver))
			},
		},
		{
			report:      "author-timeline",
			newObserver: func() CommitObserver { return newHistoryAuthorTimelineObserver(HistoryBuckets) },
			render: func(o CommitObserver) (string, error) {
				return renderAuthorTimeline(o.(*historyAuthorTimelineObserver))
			},
		},
		{
			report:      "languages-timeline",
			newObserver: func() CommitObserver { return newHistoryLanguagesObserver(HistoryBuckets) },
			render: func(o CommitObserver) (string, error) {
				return renderLanguagesTimeline(o.(*historyLanguagesObserver))
			},
		},
	}
}

var historyFormats = []string{"tabular", "csv", "json"}

func resetHistoryGlobals(t *testing.T) {
	t.Helper()
	saveDepth, saveBuckets, saveFormat, saveFileOutput, saveMore := HistoryDepth, HistoryBuckets, Format, FileOutput, More
	saveCognitive, saveCi := Cognitive, Ci
	t.Cleanup(func() {
		HistoryDepth, HistoryBuckets, Format, FileOutput, More = saveDepth, saveBuckets, saveFormat, saveFileOutput, saveMore
		Cognitive, Ci = saveCognitive, saveCi
	})
	HistoryDepth, HistoryBuckets = 1000, 60
	Format, FileOutput, More, Cognitive, Ci = "", "", false, false, false
}

// assertNoticeRendered checks that the uniform marker survives into each of
// the three output formats.
func assertNoticeRendered(t *testing.T, report, format, notice, out string) {
	t.Helper()
	switch format {
	case "tabular":
		if !strings.Contains(out, "notice: "+notice) {
			t.Errorf("%s/%s: tabular output missing notice %q:\n%s", report, format, notice, out)
		}
	case "csv":
		if !strings.Contains(out, "# notice: "+notice) {
			t.Errorf("%s/%s: csv output missing notice comment %q:\n%s", report, format, notice, out)
		}
	case "json":
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("%s/json: unmarshal: %v\n%s", report, err, out)
		}
		if got, _ := doc["notice"].(string); got != notice {
			t.Errorf("%s/json: notice = %q, want %q", report, got, notice)
		}
	}
}

func assertNoNotice(t *testing.T, report, format, out string) {
	t.Helper()
	if strings.Contains(out, "notice:") {
		t.Errorf("%s/%s: complete walk must not carry a notice:\n%s", report, format, out)
	}
	if format == "json" {
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("%s/json: unmarshal: %v", report, err)
		}
		if _, present := doc["notice"]; present {
			t.Errorf("%s/json: notice field must be omitted on a complete walk", report)
		}
	}
}

// TestHistoryWithoutGitDegrades is the core no-.git contract: every analysis
// returns a nil error, finalises with the uniform no-git notice, and the
// notice renders in every format.
func TestHistoryWithoutGitDegrades(t *testing.T) {
	pinLocale(t)
	resetHistoryGlobals(t)
	ProcessConstants()

	dir := makeNoGitDir(t, map[string]string{"a.go": "package a\nfunc A() {}\n"})

	for _, h := range historyHarnesses() {
		t.Run(h.report, func(t *testing.T) {
			obs := h.newObserver()
			window, err := runHistory(dir, obs)
			if err != nil {
				t.Fatalf("runHistory without .git must degrade, got error: %v", err)
			}
			if window.Notice != NoticeNoGit {
				t.Fatalf("window.Notice = %q, want %q", window.Notice, NoticeNoGit)
			}
			if window.Commits != 0 {
				t.Fatalf("degraded window must report zero commits, got %d", window.Commits)
			}

			for _, format := range historyFormats {
				Format = map[string]string{"tabular": "", "csv": "csv", "json": "json"}[format]
				out, err := h.render(obs)
				if err != nil {
					t.Fatalf("%s/%s render: %v", h.report, format, err)
				}
				assertNoticeRendered(t, h.report, format, NoticeNoGit, out)
			}
		})
	}
}

// TestHistoryShallowCloneAnnotated requires the shallow walk to complete,
// keep the commits it could walk, and mark the window as truncated.
func TestHistoryShallowCloneAnnotated(t *testing.T) {
	pinLocale(t)
	resetHistoryGlobals(t)

	dir, observed := makeShallowFixtureRepo(t)

	for _, h := range historyHarnesses() {
		t.Run(h.report, func(t *testing.T) {
			obs := h.newObserver()
			window, err := runHistory(dir, obs)
			if err != nil {
				t.Fatalf("shallow runHistory errored: %v", err)
			}
			if window.Notice != NoticeShallow {
				t.Fatalf("window.Notice = %q, want %q", window.Notice, NoticeShallow)
			}
			if window.Commits != observed {
				t.Fatalf("window.Commits = %d, want %d", window.Commits, observed)
			}
			for _, format := range historyFormats {
				Format = map[string]string{"tabular": "", "csv": "csv", "json": "json"}[format]
				out, err := h.render(obs)
				if err != nil {
					t.Fatalf("%s/%s render: %v", h.report, format, err)
				}
				assertNoticeRendered(t, h.report, format, NoticeShallow, out)
			}
		})
	}
}

// TestHistorySingleCommitCompletes covers the third environment: one commit
// is a complete walk, so there is no notice, every report renders in every
// format, and repeating the render is byte-identical.
func TestHistorySingleCommitCompletes(t *testing.T) {
	pinLocale(t)
	resetHistoryGlobals(t)

	dir := makeFixtureRepo(t, []map[string]string{
		{"a.go": "package a\nfunc A() {}\n"},
	})

	for _, h := range historyHarnesses() {
		t.Run(h.report, func(t *testing.T) {
			for _, format := range historyFormats {
				Format = map[string]string{"tabular": "", "csv": "csv", "json": "json"}[format]

				obs1 := h.newObserver()
				window, err := runHistory(dir, obs1)
				if err != nil {
					t.Fatalf("%s: single-commit runHistory errored: %v", h.report, err)
				}
				if window.Commits != 1 {
					t.Fatalf("%s: window.Commits = %d, want 1", h.report, window.Commits)
				}
				if window.Notice != "" {
					t.Fatalf("%s: single complete commit must not be marked degraded, got %q", h.report, window.Notice)
				}
				out1, err := h.render(obs1)
				if err != nil {
					t.Fatalf("%s/%s render: %v", h.report, format, err)
				}
				assertNoNotice(t, h.report, format, out1)

				obs2 := h.newObserver()
				if _, err := runHistory(dir, obs2); err != nil {
					t.Fatalf("%s: second run errored: %v", h.report, err)
				}
				out2, err := h.render(obs2)
				if err != nil {
					t.Fatalf("%s/%s second render: %v", h.report, format, err)
				}
				if out1 != out2 {
					t.Fatalf("%s/%s is not deterministic across runs", h.report, format)
				}
			}
		})
	}
}

// TestHistoryEmptyNoGitDir covers both an empty directory and a one-file
// directory without git: analysis degrades identically and never panics.
func TestHistoryEmptyNoGitDir(t *testing.T) {
	pinLocale(t)
	resetHistoryGlobals(t)
	ProcessConstants()

	cases := map[string]string{
		"empty":       makeNoGitDir(t, nil),
		"single file": makeNoGitDir(t, map[string]string{"only.go": "package only\n"}),
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			for _, h := range historyHarnesses() {
				obs := h.newObserver()
				window, err := runHistory(dir, obs)
				if err != nil {
					t.Fatalf("%s/%s: expected graceful degradation, got %v", name, h.report, err)
				}
				if window.Notice != NoticeNoGit {
					t.Fatalf("%s/%s: notice = %q, want %q", name, h.report, window.Notice, NoticeNoGit)
				}
				Format = "json"
				out, err := h.render(obs)
				if err != nil {
					t.Fatalf("%s/%s render: %v", name, h.report, err)
				}
				if !strings.Contains(out, NoticeNoGit) {
					t.Fatalf("%s/%s: json output missing notice", name, h.report)
				}
			}
		})
	}
}

// captureStdout swaps os.Stdout for a pipe while fn runs and returns
// everything fn printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// TestHistoryRunnersDegradeEndToEnd drives the CLI-level runner functions in
// a no-git directory: they must return nil (Process turns a non-nil error
// into exit 1) and still print the annotated empty report.
func TestHistoryRunnersDegradeEndToEnd(t *testing.T) {
	pinLocale(t)
	resetHistoryGlobals(t)
	ProcessConstants()

	dir := makeNoGitDir(t, map[string]string{"a.go": "package a\n"})
	runners := map[string]func(string) error{
		"hotspots":           runHotspotsReport,
		"coupling":           runCouplingReport,
		"authors":            runAuthorsReport,
		"author-timeline":    runAuthorTimelineReport,
		"languages-timeline": runLanguagesTimelineReport,
	}
	for name, run := range runners {
		t.Run(name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() { err = run(dir) })
			if err != nil {
				t.Fatalf("%s runner must not fail without .git, got: %v", name, err)
			}
			if !strings.Contains(out, NoticeNoGit) {
				t.Fatalf("%s runner output missing degradation notice:\n%s", name, out)
			}
		})
	}
}

// TestDegradationDoesNotAffectWorkingTreeStats checks the report path: with
// no git available the working-tree counts are still produced, history
// sections stay empty, and a second run yields identical totals.
func TestDegradationDoesNotAffectWorkingTreeStats(t *testing.T) {
	pinLocale(t)
	resetHistoryGlobals(t)
	ProcessConstants()

	dir := makeNoGitDir(t, map[string]string{
		"a.go": "package a\nfunc A() { if true {} }\n",
		"b.py": "print('x')\n",
	})

	first, err := CollectReportData(dir)
	if err != nil {
		t.Fatalf("CollectReportData without .git: %v", err)
	}
	if first.GitAvailable {
		t.Fatal("GitAvailable must be false without .git")
	}
	if first.Totals.Files != 2 || first.Totals.Lines == 0 {
		t.Fatalf("working-tree totals lost under degradation: %+v", first.Totals)
	}
	if first.Hotspots != nil || first.Coupling != nil || first.Authors != nil ||
		first.LanguageTimeline != nil || first.AuthorTimeline != nil {
		t.Fatalf("history sections must be empty without git, got %+v %+v %+v",
			first.Hotspots, first.Coupling, first.Authors)
	}
	if len(first.Summary) == 0 {
		t.Fatal("language summary must still be produced without git")
	}

	// Process() clears the shared visited-path cache between runs; mirror
	// that here so the second walk is not treated as a re-visit.
	cleanVisitedPaths()
	second, err := CollectReportData(dir)
	if err != nil {
		t.Fatalf("second CollectReportData: %v", err)
	}
	if second.Totals != first.Totals {
		t.Fatalf("totals not stable across runs: %+v vs %+v", second.Totals, first.Totals)
	}
}

// TestHistoryWriteFailureNamesReportAndPath requires a failed history -o
// write to surface both the report/format label and the target path.
func TestHistoryWriteFailureNamesReportAndPath(t *testing.T) {
	pinLocale(t)
	resetHistoryGlobals(t)

	dir := makeFixtureRepo(t, []map[string]string{
		{"a.go": "package a\nfunc A() {}\n"},
	})
	bad := filepath.Join(t.TempDir(), "missing", "out.json")
	FileOutput = bad
	Format = "json"

	err := runHotspotsReport(dir)
	if err == nil {
		t.Fatal("expected a write error")
	}
	msg := err.Error()
	if !strings.Contains(msg, bad) {
		t.Errorf("error missing path: %s", msg)
	}
	if !strings.Contains(msg, "hotspots") {
		t.Errorf("error missing report label: %s", msg)
	}
	if !strings.Contains(msg, "json") {
		t.Errorf("error missing format label: %s", msg)
	}
}

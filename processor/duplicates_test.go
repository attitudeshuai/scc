// SPDX-License-Identifier: MIT

package processor

import "testing"

// TestDuplicatesClearedBetweenRuns guards against per-run state leaking into
// the next run. The deterministic dedupe filter holds its election state in a
// goroutine-local map created fresh for every scan, so a second ProcessResult
// call must count the same tree the same way rather than treating the first
// run's files as already seen.
func TestDuplicatesClearedBetweenRuns(t *testing.T) {
	savedDuplicates := Duplicates
	savedPaths := DirFilePaths
	savedAllow, savedExclude := AllowListExtensions, ExcludeListExtensions
	defer func() {
		Duplicates = savedDuplicates
		DirFilePaths = savedPaths
		AllowListExtensions, ExcludeListExtensions = savedAllow, savedExclude
	}()

	Duplicates = true
	AllowListExtensions = []string{}
	ExcludeListExtensions = []string{}

	run := func() []LanguageSummary {
		t.Helper()
		DirFilePaths = []string{"../examples/duplicates"}
		summary, err := ProcessResult()
		if err != nil {
			t.Fatalf("ProcessResult: %s", err)
		}
		return summary
	}

	first := run()
	if len(first) == 0 {
		t.Fatal("first run counted nothing, test cannot detect the leak")
	}

	second := run()
	if len(second) != len(first) {
		t.Fatalf("duplicate state leaked between runs: first run counted %d languages, second counted %d", len(second), len(first))
	}
	for i := range first {
		if first[i].Count != second[i].Count {
			t.Errorf("language %s: first run counted %d files, second counted %d", first[i].Name, first[i].Count, second[i].Count)
		}
	}
}

// TestDedupeDuplicateJobsElectsSmallestPath pins the deterministic election:
// the filter keeps one job per content hash and the survivor is the member
// with the smallest normalized path, independent of arrival order.
func TestDedupeDuplicateJobsElectsSmallestPath(t *testing.T) {
	hash := []byte("same-content")
	jobs := []*FileJob{
		{Location: "c/z.go", Filename: "z.go", contentHash: hash, Lines: 3},
		{Location: "a/m.go", Filename: "m.go", contentHash: hash, Lines: 3},
		{Location: "b/n.go", Filename: "n.go", contentHash: hash, Lines: 3},
		{Location: "unique.go", Filename: "unique.go", contentHash: []byte("other"), Lines: 9},
		{Location: "no-hash.go", Filename: "no-hash.go"}, // defensive passthrough
	}

	input := make(chan *FileJob, len(jobs))
	for _, job := range jobs {
		input <- job
	}
	close(input)

	output := dedupeDuplicateJobs(input)
	var got []*FileJob
	for job := range output {
		got = append(got, job)
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 survivors (one duplicate set, one unique, one passthrough), got %d", len(got))
	}

	// Output is in canonical path order.
	wantOrder := []string{"a/m.go", "no-hash.go", "unique.go"}
	for i, want := range wantOrder {
		if got[i].Location != want {
			t.Errorf("survivor %d = %q, want %q", i, got[i].Location, want)
		}
	}
}

// TestDedupeDuplicateJobsArrivalIndependent feeds the same duplicate set in
// both orders and requires the same survivor and the same output order.
func TestDedupeDuplicateJobsArrivalIndependent(t *testing.T) {
	hash := []byte("hash")
	build := func(locations ...string) []*FileJob {
		jobs := make([]*FileJob, 0, len(locations))
		for _, loc := range locations {
			jobs = append(jobs, &FileJob{Location: loc, Filename: loc, contentHash: hash})
		}
		return jobs
	}

	run := func(jobs []*FileJob) []string {
		input := make(chan *FileJob, len(jobs))
		for _, job := range jobs {
			input <- job
		}
		close(input)

		var locations []string
		for job := range dedupeDuplicateJobs(input) {
			locations = append(locations, job.Location)
		}
		return locations
	}

	one := run(build("z/1.go", "a/2.go", "m/3.go"))
	two := run(build("m/3.go", "z/1.go", "a/2.go"))

	if len(one) != 1 || one[0] != "a/2.go" {
		t.Fatalf("first order elected %v, want only a/2.go", one)
	}
	if len(two) != 1 || two[0] != "a/2.go" {
		t.Fatalf("second order elected %v, want only a/2.go", two)
	}
}

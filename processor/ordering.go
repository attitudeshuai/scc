// SPDX-License-Identifier: MIT

package processor

import (
	"path/filepath"
	"slices"
	"strings"
)

// normalizedPath puts a file location in the one canonical form every output
// order is defined against. Clean collapses "./" and ".." segments and ToSlash
// removes the platform separator from the comparison, so a run on Windows,
// macOS and Linux orders the same files the same way. The walking and language
// detection rules are untouched; this only names the order results print in.
func normalizedPath(location string) string {
	return filepath.ToSlash(filepath.Clean(location))
}

// compareNormalizedPath is the total ordering per-file outputs share. Sorting
// every per-file record with it is what makes a concurrent scan byte-identical
// to a serial one and independent of how many workers happened to finish first.
func compareNormalizedPath(a, b string) int {
	return strings.Compare(normalizedPath(a), normalizedPath(b))
}

// compareFileJobs orders jobs by their normalized location. Equal locations
// compare equal rather than falling back to another field, because two counted
// jobs never legitimately share one location within a run.
func compareFileJobs(a, b *FileJob) int {
	return compareNormalizedPath(a.Location, b.Location)
}

// sortFileJobsByPath sorts a flat job slice into the canonical path order.
// The sort is in place: callers that already retain the jobs for printing pay
// no extra per-job allocation beyond the sort's bookkeeping.
func sortFileJobsByPath(jobs []*FileJob) {
	slices.SortFunc(jobs, compareFileJobs)
}

// sortLanguageFilesByPath puts the retained files of every language into
// canonical path order. Summary outputs that keep no per-file records hold nil
// slices, so the default (folded) run retains nothing extra here.
func sortLanguageFilesByPath(language []LanguageSummary) {
	for i := range language {
		if len(language[i].Files) > 1 {
			sortFileJobsByPath(language[i].Files)
		}
	}
}

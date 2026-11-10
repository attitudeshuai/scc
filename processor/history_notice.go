// SPDX-License-Identifier: MIT

package processor

import "strings"

// Uniform history-degradation notices. Every history analysis degrades to an
// empty window instead of erroring when the git data it needs is missing, and
// every rendered report (tabular, CSV, JSON) annotates that with the same
// sentence. Keeping the wording in one place is what makes the degradation
// marker identical across the five analyses and the three formats.
const (
	// NoticeNoGit marks a path that is not inside a git repository at all.
	NoticeNoGit = "git repository not found; history analysis unavailable"
	// NoticeNoCommits marks a repository whose HEAD does not resolve (an
	// unborn branch, nothing committed yet).
	NoticeNoCommits = "no commits in repository; history analysis empty"
	// NoticeShallow marks a shallow clone whose walk stopped at the shallow
	// boundary: the window holds partial history, not the full chain.
	NoticeShallow = "shallow clone; history truncated at the shallow boundary"
)

// historyNoticeLine renders the tabular annotation line. It sits directly
// under the report header so the empty table that follows is never mistaken
// for a repo that genuinely has no activity.
func historyNoticeLine(notice string) string {
	if notice == "" {
		return ""
	}
	return "notice: " + notice + "\n"
}

// historyNoticeComment renders the CSV annotation as a comment line next to
// the window comment, keeping the machine-readable header block together.
func historyNoticeComment(notice string) string {
	if notice == "" {
		return ""
	}
	return "# notice: " + notice + "\n"
}

// formatLabel canonicalises a history --format value for write-error
// messages: an empty format is the default tabular view.
func formatLabel(format string) string {
	if format == "" {
		return "tabular"
	}
	return strings.ToLower(format)
}

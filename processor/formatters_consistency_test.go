// SPDX-License-Identifier: MIT

// These tests pin the cross-format contract for one and the same input:
//
//   - every format reports the same totals;
//   - every format that carries a percentage/mean carries the same number,
//     produced by one rounding rule;
//   - equal sort keys are always decided by name so repeated runs are
//     byte-identical.
//
// They are the red side of the consistency work: on the unmodified code the
// JSON percentages carry full float precision while tabular rounds them, the
// per-file sorts have no name tie-break, and openmetrics --by-file follows
// nondeterministic channel order.

package processor

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// consistencyJob builds one counted file job. Content is left nil: the
// formatters only read the aggregated counters, so no disk or classifier is
// involved and every format sees exactly the same numbers.
func consistencyJob(language, location string, lines, code, comment, blank, complexity, bytes int64) *FileJob {
	return &FileJob{
		Language:   language,
		Filename:   location[strings.LastIndex(location, "/")+1:],
		Extension:  strings.TrimPrefix(location[strings.LastIndex(location, "."):], "."),
		Location:   location,
		Lines:      lines,
		Code:       code,
		Comment:    comment,
		Blank:      blank,
		Complexity: complexity,
		Bytes:      bytes,
	}
}

// consistencyFixture is seven files in three languages. The counts split
// into non-terminating decimals (3/2/2 of seven files, lines in thirds) so a
// rounding disagreement cannot hide, and four Go files share identical
// counters on purpose so the sort tie-break is exercised.
func consistencyFixture() []*FileJob {
	return []*FileJob{
		consistencyJob("Go", "proj/z.go", 9, 6, 1, 2, 2, 120),
		consistencyJob("Go", "proj/a.go", 9, 6, 1, 2, 2, 120),
		consistencyJob("Go", "proj/m.go", 9, 6, 1, 2, 2, 120),
		consistencyJob("Go", "proj/n.go", 9, 6, 1, 2, 2, 120),
		consistencyJob("Python", "proj/x.py", 6, 5, 0, 1, 1, 80),
		consistencyJob("Python", "proj/y.py", 3, 3, 0, 0, 0, 40),
		consistencyJob("Markdown", "proj/r.md", 3, 3, 0, 0, 0, 30),
		consistencyJob("Markdown", "proj/q.md", 3, 2, 0, 1, 0, 25),
	}
}

func fixtureChan(jobs []*FileJob) chan *FileJob {
	c := make(chan *FileJob, len(jobs))
	for _, j := range jobs {
		c <- j
	}
	close(c)
	return c
}

// shuffledChan returns a fresh channel holding the jobs in a reversed
// copy, so any sort that depends on arrival order rather than keys shows up.
func shuffledChan(jobs []*FileJob) chan *FileJob {
	rev := slices.Clone(jobs)
	slices.Reverse(rev)
	return fixtureChan(rev)
}

// formatTotals is the cross-format comparison shape. Fields a format cannot
// express stay nil, so each format is compared on the figures it actually
// emits rather than against invented zeros.
type formatTotals struct {
	Files      int64
	Lines      int64
	Code       int64
	Comment    int64
	Blank      int64
	Complexity int64
	Bytes      int64
}

// resetFormatGlobals restores every package flag the formatters read and the
// ULOC accumulator maps, so one test's flags cannot leak into another.
func resetFormatGlobals(t *testing.T) {
	t.Helper()
	saved := map[string]any{
		"Format": Format, "SortBy": SortBy, "Files": Files, "Percent": Percent,
		"MaxMean": MaxMean, "Cognitive": Cognitive, "Complexity": Complexity,
		"More": More, "Ci": Ci, "HBorder": HBorder, "UlocMode": UlocMode,
		"Dryness": Dryness, "Cocomo": Cocomo, "Size": Size, "Locomo": Locomo,
		"FormatMulti": FormatMulti, "FileOutput": FileOutput,
	}
	t.Cleanup(func() {
		Format = saved["Format"].(string)
		SortBy = saved["SortBy"].(string)
		Files = saved["Files"].(bool)
		Percent = saved["Percent"].(bool)
		MaxMean = saved["MaxMean"].(bool)
		Cognitive = saved["Cognitive"].(bool)
		Complexity = saved["Complexity"].(bool)
		More = saved["More"].(bool)
		Ci = saved["Ci"].(bool)
		HBorder = saved["HBorder"].(bool)
		UlocMode = saved["UlocMode"].(bool)
		Dryness = saved["Dryness"].(bool)
		Cocomo = saved["Cocomo"].(bool)
		Size = saved["Size"].(bool)
		Locomo = saved["Locomo"].(bool)
		FormatMulti = saved["FormatMulti"].(string)
		FileOutput = saved["FileOutput"].(string)
	})

	Format, SortBy, FormatMulti, FileOutput = "", "", "", ""
	Files, Percent, MaxMean, Cognitive, More, Ci, HBorder = false, false, false, false, false, false, false
	UlocMode, Dryness, Locomo = false, false, false
	// Suppress the cost/size trailer so Total is the last numeric row.
	Cocomo, Size = true, true

	ulocMutex.Lock()
	ulocGlobalCount = map[string]struct{}{}
	ulocLanguageCount = map[string]map[string]struct{}{}
	ulocMutex.Unlock()
}

var integerRun = regexp.MustCompile(`-?\d+`)

func atoi64(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return n
}

// parseTabularTotals pulls the seven counters off the Total row of either
// tabular layout. The row begins with the literal "Total".
func parseTabularTotals(t *testing.T, out string) formatTotals {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "Total") {
			continue
		}
		nums := integerRun.FindAllString(line, -1)
		if len(nums) < 6 {
			t.Fatalf("Total row missing counters: %q", line)
		}
		// short/wide column order: Files Lines Blank Comment Code Complexity
		// (the tabular layouts carry no Bytes column).
		return formatTotals{
			Files:      atoi64(t, nums[0]),
			Lines:      atoi64(t, nums[1]),
			Blank:      atoi64(t, nums[2]),
			Comment:    atoi64(t, nums[3]),
			Code:       atoi64(t, nums[4]),
			Complexity: atoi64(t, nums[5]),
		}
	}
	t.Fatalf("no Total row in:\n%s", out)
	return formatTotals{}
}

type jsonLanguageRow struct {
	Name       string `json:"Name"`
	Lines      int64  `json:"Lines"`
	Code       int64  `json:"Code"`
	Comment    int64  `json:"Comment"`
	Blank      int64  `json:"Blank"`
	Complexity int64  `json:"Complexity"`
	Bytes      int64  `json:"Bytes"`
	Count      int64  `json:"Count"`
}

func parseJSONTotals(t *testing.T, out string) formatTotals {
	t.Helper()
	var rows []jsonLanguageRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal json: %v\n%s", err, out)
	}
	var got formatTotals
	for _, r := range rows {
		got.Files += r.Count
		got.Lines += r.Lines
		got.Code += r.Code
		got.Comment += r.Comment
		got.Blank += r.Blank
		got.Complexity += r.Complexity
		got.Bytes += r.Bytes
	}
	return got
}

func parseJSON2Totals(t *testing.T, out string) formatTotals {
	t.Helper()
	var doc struct {
		LanguageSummary []jsonLanguageRow `json:"languageSummary"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unmarshal json2: %v", err)
	}
	var got formatTotals
	for _, r := range doc.LanguageSummary {
		got.Files += r.Count
		got.Lines += r.Lines
		got.Code += r.Code
		got.Comment += r.Comment
		got.Blank += r.Blank
		got.Complexity += r.Complexity
		got.Bytes += r.Bytes
	}
	return got
}

func parseCSVTotals(t *testing.T, out string) formatTotals {
	t.Helper()
	records, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	var got formatTotals
	for i, rec := range records {
		if i == 0 || len(rec) < 9 {
			continue
		}
		// One row per language; the Files column (index 7) holds the count.
		got.Files += atoi64(t, rec[7])
		got.Lines += atoi64(t, rec[1])
		got.Code += atoi64(t, rec[2])
		got.Comment += atoi64(t, rec[3])
		got.Blank += atoi64(t, rec[4])
		got.Complexity += atoi64(t, rec[5])
		got.Bytes += atoi64(t, rec[6])
	}
	return got
}

func parseClocYAMLTotals(t *testing.T, out string) formatTotals {
	t.Helper()
	var got formatTotals
	// The per-language map repeats the same key names as the SUM block, so
	// only the header (n_files/n_lines) and the SUM section are read.
	inSum := false
	for _, line := range strings.Split(out, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "SUM:" {
			inSum = true
			continue
		}
		switch {
		case strings.HasPrefix(trim, "n_files:"):
			got.Files = atoi64(t, strings.Fields(trim)[1])
		case strings.HasPrefix(trim, "n_lines:"):
			got.Lines = atoi64(t, strings.Fields(trim)[1])
		case inSum && strings.HasPrefix(trim, "code:"):
			got.Code = atoi64(t, strings.Fields(trim)[1])
		case inSum && strings.HasPrefix(trim, "comment:"):
			got.Comment = atoi64(t, strings.Fields(trim)[1])
		case inSum && strings.HasPrefix(trim, "blank:"):
			got.Blank = atoi64(t, strings.Fields(trim)[1])
		}
	}
	return got
}

var htmlTotalRow = regexp.MustCompile(`(?s)<tfoot>.*?<th>Total</th>(.*?)</tr>`)

func parseHTMLTotals(t *testing.T, out string) formatTotals {
	t.Helper()
	m := htmlTotalRow.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no html tfoot Total row")
	}
	nums := integerRun.FindAllString(m[1], -1)
	if len(nums) < 7 {
		t.Fatalf("html Total row missing counters: %v", nums)
	}
	// column order: Files Lines Blank Comment Code Complexity Bytes (ULOC last)
	return formatTotals{
		Files:      atoi64(t, nums[0]),
		Lines:      atoi64(t, nums[1]),
		Blank:      atoi64(t, nums[2]),
		Comment:    atoi64(t, nums[3]),
		Code:       atoi64(t, nums[4]),
		Complexity: atoi64(t, nums[5]),
		Bytes:      atoi64(t, nums[6]),
	}
}

var openMetricsSample = regexp.MustCompile(`^scc_([a-z]+)\{[^}]*\} (\d+)$`)

func parseOpenMetricsTotals(t *testing.T, out string) formatTotals {
	t.Helper()
	var got formatTotals
	for _, line := range strings.Split(out, "\n") {
		m := openMetricsSample.FindStringSubmatch(line)
		if m == nil || strings.Contains(line, "file=") {
			continue // summary samples only
		}
		v := atoi64(t, m[2])
		switch m[1] {
		case "files":
			got.Files += v
		case "lines":
			got.Lines += v
		case "code":
			got.Code += v
		case "comments":
			got.Comment += v
		case "blanks":
			got.Blank += v
		case "complexity":
			got.Complexity += v
		case "bytes":
			got.Bytes += v
		}
	}
	return got
}

var sqlInsertRow = regexp.MustCompile(`insert into t values\(.*?(-?\d+), (-?\d+), (-?\d+), (-?\d+), (-?\d+), (-?\d+)(?:, -?\d+)?\);`)

func parseSQLTotals(t *testing.T, out string) formatTotals {
	t.Helper()
	var got formatTotals
	// nByte, nBlank, nComment, nCode, nComplexity, nUloc (nCognitive optional)
	for _, m := range sqlInsertRow.FindAllStringSubmatch(out, -1) {
		got.Files++
		got.Bytes += atoi64(t, m[1])
		got.Blank += atoi64(t, m[2])
		got.Comment += atoi64(t, m[3])
		got.Code += atoi64(t, m[4])
		got.Complexity += atoi64(t, m[5])
	}
	return got
}

// TestCrossFormatTotalsAgree renders every summary format from identical
// input and requires the counters each format does emit to equal the
// tabular Total row.
func TestCrossFormatTotalsAgree(t *testing.T) {
	pinLocale(t)
	resetFormatGlobals(t)
	jobs := consistencyFixture()

	render := func(name string) string {
		switch name {
		case "tabular":
			return fileSummarizeShort(fixtureChan(jobs))
		case "wide":
			return fileSummarizeLong(fixtureChan(jobs))
		case "json":
			return toJSON(fixtureChan(jobs))
		case "json2":
			return toJSON2(fixtureChan(jobs))
		case "csv":
			return toCSV(fixtureChan(jobs))
		case "cloc-yaml":
			return toClocYAML(fixtureChan(jobs))
		case "html":
			return toHtml(fixtureChan(jobs))
		case "html-table":
			return toHtmlTable(fixtureChan(jobs))
		case "openmetrics":
			return toOpenMetrics(fixtureChan(jobs))
		case "sql":
			return toSql(fixtureChan(jobs))
		case "sql-insert":
			return toSqlInsert(fixtureChan(jobs))
		default:
			t.Fatalf("unknown format %s", name)
			return ""
		}
	}

	// JSON expresses every counter including Bytes, so it is the canonical
	// totals vector; every other format is compared on the fields it emits.
	want := parseJSONTotals(t, render("json"))
	tabular := parseTabularTotals(t, render("tabular"))
	for _, field := range []string{"Files", "Lines", "Blank", "Comment", "Code", "Complexity"} {
		if valueOf(tabular, field) != valueOf(want, field) {
			t.Errorf("tabular %s = %d, want json total %d", field, valueOf(tabular, field), valueOf(want, field))
		}
	}

	cases := []struct {
		name   string
		parse  func(*testing.T, string) formatTotals
		fields []string // counters this format is able to express
	}{
		{"wide", parseTabularTotals, []string{"Files", "Lines", "Blank", "Comment", "Code", "Complexity"}},
		{"json2", parseJSON2Totals, []string{"Files", "Lines", "Blank", "Comment", "Code", "Complexity", "Bytes"}},
		{"csv", parseCSVTotals, []string{"Files", "Lines", "Blank", "Comment", "Code", "Complexity", "Bytes"}},
		{"cloc-yaml", parseClocYAMLTotals, []string{"Files", "Lines", "Blank", "Comment", "Code"}},
		{"html", parseHTMLTotals, []string{"Files", "Lines", "Blank", "Comment", "Code", "Complexity", "Bytes"}},
		{"html-table", parseHTMLTotals, []string{"Files", "Lines", "Blank", "Comment", "Code", "Complexity", "Bytes"}},
		{"openmetrics", parseOpenMetricsTotals, []string{"Files", "Lines", "Blank", "Comment", "Code", "Complexity", "Bytes"}},
		{"sql", parseSQLTotals, []string{"Files", "Blank", "Comment", "Code", "Complexity", "Bytes"}},
		{"sql-insert", parseSQLTotals, []string{"Files", "Blank", "Comment", "Code", "Complexity", "Bytes"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.parse(t, render(tc.name))
			for _, field := range tc.fields {
				if valueOf(got, field) != valueOf(want, field) {
					t.Errorf("%s %s = %d, want json total %d", tc.name, field, valueOf(got, field), valueOf(want, field))
				}
			}
		})
	}
}

func valueOf(f formatTotals, field string) int64 {
	switch field {
	case "Files":
		return f.Files
	case "Lines":
		return f.Lines
	case "Blank":
		return f.Blank
	case "Comment":
		return f.Comment
	case "Code":
		return f.Code
	case "Complexity":
		return f.Complexity
	case "Bytes":
		return f.Bytes
	}
	return 0
}

// pctCell is one language's percentage row in the six metric columns.
type pctCell struct {
	Name                                 string
	File, Line, Blank, Comment, Code, Cx float64
}

func parseTabularPercentRows(t *testing.T, out string) []pctCell {
	t.Helper()
	lines := strings.Split(out, "\n")
	var rows []pctCell
	var lastName string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "─") || strings.HasPrefix(trim, "-") {
			continue
		}
		fields := strings.Fields(trim)
		if len(fields) >= 7 && fields[0] != "Percentage" && fields[0] != "Total" && fields[0] != "Language" {
			lastName = fields[0]
		}
		if fields[0] == "Percentage" {
			nums := make([]float64, 0, 6)
			for _, f := range fields[1:] {
				v, err := strconv.ParseFloat(strings.TrimSuffix(f, "%"), 64)
				if err != nil {
					t.Fatalf("parse percentage %q: %v", f, err)
				}
				nums = append(nums, v)
			}
			if len(nums) != 6 {
				t.Fatalf("percentage row has %d columns, want 6: %q", len(nums), line)
			}
			rows = append(rows, pctCell{
				Name: lastName, File: nums[0], Line: nums[1], Blank: nums[2],
				Comment: nums[3], Code: nums[4], Cx: nums[5],
			})
		}
	}
	return rows
}

type jsonPctRow struct {
	Name              string   `json:"Name"`
	FilePercent       *float64 `json:"FilePercent"`
	LinePercent       *float64 `json:"LinePercent"`
	BlankPercent      *float64 `json:"BlankPercent"`
	CommentPercent    *float64 `json:"CommentPercent"`
	CodePercent       *float64 `json:"CodePercent"`
	ComplexityPercent *float64 `json:"ComplexityPercent"`
}

func parseJSONPercentRows(t *testing.T, out string) []pctCell {
	t.Helper()
	var rows []jsonPctRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal json percentages: %v", err)
	}
	out2 := make([]pctCell, 0, len(rows))
	for _, r := range rows {
		out2 = append(out2, pctCell{
			Name: r.Name, File: *r.FilePercent, Line: *r.LinePercent, Blank: *r.BlankPercent,
			Comment: *r.CommentPercent, Code: *r.CodePercent, Cx: *r.ComplexityPercent,
		})
	}
	return out2
}

// TestCrossFormatPercentagesUnified requires the JSON/json2 percentage for
// every language and metric to be exactly the one-decimal number the
// tabular views print. On the unmodified code JSON carries the raw
// float64 ratio (85.71428571428571 vs 85.7), so this fails.
func TestCrossFormatPercentagesUnified(t *testing.T) {
	pinLocale(t)
	resetFormatGlobals(t)
	Percent = true
	jobs := consistencyFixture()

	short := parseTabularPercentRows(t, fileSummarizeShort(fixtureChan(jobs)))
	wide := parseTabularPercentRows(t, fileSummarizeLong(fixtureChan(jobs)))
	js := parseJSONPercentRows(t, toJSON(fixtureChan(jobs)))

	if len(short) != len(js) || len(wide) != len(js) {
		t.Fatalf("row counts short=%d wide=%d json=%d", len(short), len(wide), len(js))
	}

	byName := func(rows []pctCell) map[string]pctCell {
		m := map[string]pctCell{}
		for _, r := range rows {
			m[r.Name] = r
		}
		return m
	}
	shortM, wideM, jsM := byName(short), byName(wide), byName(js)

	for name, tab := range shortM {
		j, ok := jsM[name]
		if !ok {
			t.Fatalf("json missing language %s", name)
		}
		w := wideM[name]
		for _, pair := range []struct {
			metric     string
			tab, w, js float64
		}{
			{"files", tab.File, w.File, j.File},
			{"lines", tab.Line, w.Line, j.Line},
			{"blanks", tab.Blank, w.Blank, j.Blank},
			{"comments", tab.Comment, w.Comment, j.Comment},
			{"code", tab.Code, w.Code, j.Code},
			{"complexity", tab.Cx, w.Cx, j.Cx},
		} {
			if fmt.Sprintf("%.1f", pair.js) != fmt.Sprintf("%.1f", pair.tab) {
				t.Errorf("%s %s: json=%v but tabular=%.1f", name, pair.metric, pair.js, pair.tab)
			}
			if fmt.Sprintf("%.1f", pair.w) != fmt.Sprintf("%.1f", pair.tab) {
				t.Errorf("%s %s: wide=%.1f but short=%.1f", name, pair.metric, pair.w, pair.tab)
			}
			// The machine-readable value must already be at one decimal:
			// no hidden precision that another consumer could round differently.
			if pair.js != roundTo1(pair.js) {
				t.Errorf("%s %s: json percentage %v carries more than one decimal place", name, pair.metric, pair.js)
			}
		}
	}
}

// TestUnifiedRoundingRule pins the shared rule against fmt's %.1f over a
// wide sweep of realistic integer ratios, which is what keeps the existing
// tabular output byte-identical once JSON starts using the same helper.
func TestUnifiedRoundingRule(t *testing.T) {
	for _, total := range []int64{1, 2, 3, 7, 8, 13, 100, 255, 1000, 1024, 4096, 10000, 65537, 1_000_000} {
		for value := int64(0); value <= total; value++ {
			got := formatFloat1(percentRounded(value, total))
			want := fmt.Sprintf("%.1f", pct(value, total))
			if got != want {
				t.Fatalf("percentRounded(%d,%d)=%s, want %.1f rule %s", value, total, got, pct(value, total), want)
			}
		}
	}
	// zero total must degrade to zero, never NaN.
	if got := percentRounded(1, 0); got != 0 {
		t.Fatalf("percentRounded with zero total = %v, want 0", got)
	}
}

// tiedFileOrder extracts the per-file location order from a rendered
// by-file output.
func tiedFileOrder(t *testing.T, format, out string) []string {
	t.Helper()
	switch format {
	case "tabular", "wide":
		// File rows begin with the (possibly padded or trimmed) location.
		// Collect location tokens in document order by regex rather than
		// splitting columns, since the name cell is padded to fixed width.
		var locs []string
		for _, m := range regexp.MustCompile(`(?:~)?(proj/[a-z]\.go)`).FindAllStringSubmatch(out, -1) {
			locs = append(locs, m[1])
		}
		return locs
	case "csv":
		records, err := csv.NewReader(strings.NewReader(out)).ReadAll()
		if err != nil {
			t.Fatalf("csv: %v", err)
		}
		var locs []string
		for i, rec := range records {
			if i == 0 {
				continue
			}
			locs = append(locs, rec[1])
		}
		return locs
	case "openmetrics":
		var locs []string
		seen := map[string]bool{}
		for _, line := range strings.Split(out, "\n") {
			m := regexp.MustCompile(`file="([^"]+)"`).FindStringSubmatch(line)
			if m != nil && !seen[m[1]] {
				seen[m[1]] = true
				locs = append(locs, m[1])
			}
		}
		return locs
	case "html":
		var locs []string
		for _, m := range regexp.MustCompile(`(?m)<td>([^<]+\.go)</td>`).FindAllStringSubmatch(out, -1) {
			locs = append(locs, m[1])
		}
		return locs
	}
	t.Fatalf("unknown format %s", format)
	return nil
}

// TestStableTieBreakByName feeds equal-counter files in reversed order and
// requires every by-file format to order the tied group by location, on
// every iteration. openmetrics currently follows channel order and the
// tabular/csv sorts lack a name tie-break, so this fails before the fix.
func TestStableTieBreakByName(t *testing.T) {
	pinLocale(t)
	resetFormatGlobals(t)
	Files = true
	jobs := consistencyFixture()

	formats := []string{"tabular", "wide", "csv", "openmetrics", "html"}
	want := []string{"proj/a.go", "proj/m.go", "proj/n.go", "proj/z.go"}

	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			for iteration := 0; iteration < 10; iteration++ {
				var out string
				switch format {
				case "tabular":
					out = fileSummarizeShort(shuffledChan(jobs))
				case "wide":
					out = fileSummarizeLong(shuffledChan(jobs))
				case "csv":
					out = toCSV(shuffledChan(jobs))
				case "openmetrics":
					out = toOpenMetrics(shuffledChan(jobs))
				case "html":
					out = toHtmlTable(shuffledChan(jobs))
				}
				order := tiedFileOrder(t, format, out)
				var got []string
				for _, loc := range order {
					if strings.HasSuffix(loc, ".go") {
						got = append(got, loc)
					}
				}
				if !slices.Equal(got, want) {
					t.Fatalf("iteration %d: tied Go files ordered %v, want %v", iteration, got, want)
				}
			}
		})
	}
}

// volatileLine strips the wall-clock bearing lines that only exist in
// cloc-yaml and the SQL metadata block; everything else in those formats is
// deterministic.
func volatileLine(line string) bool {
	for _, prefix := range []string{"elapsed_seconds:", "files_per_second:", "lines_per_second:"} {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return true
		}
	}
	if strings.Contains(line, "insert into metadata values(") {
		return true
	}
	return false
}

func stripVolatile(out string) string {
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		if !volatileLine(line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// TestRepeatedRendersByteIdentical renders each format twice from the same
// ordered input and requires identical bytes, for a normal fixture, an empty
// input and a single-file input.
func TestRepeatedRendersByteIdentical(t *testing.T) {
	pinLocale(t)
	resetFormatGlobals(t)

	full := consistencyFixture()
	single := full[:1]
	var none []*FileJob

	// csv-stream writes straight to stdout as rows arrive and has its own
	// dedicated tests, so it is not part of this in-memory comparison.
	renderers := map[string]func(chan *FileJob) string{
		"tabular":     fileSummarizeShort,
		"wide":        fileSummarizeLong,
		"json":        toJSON,
		"json2":       toJSON2,
		"csv":         toCSV,
		"cloc-yaml":   toClocYAML,
		"html":        toHtml,
		"html-table":  toHtmlTable,
		"openmetrics": toOpenMetrics,
		"sql":         toSql,
		"sql-insert":  toSqlInsert,
	}

	for _, fixture := range []struct {
		name string
		jobs []*FileJob
	}{
		{"full", full}, {"single", single}, {"empty", none},
	} {
		for format, fn := range renderers {
			t.Run(fixture.name+"/"+format, func(t *testing.T) {
				first := fn(fixtureChan(slices.Clone(fixture.jobs)))
				second := fn(fixtureChan(slices.Clone(fixture.jobs)))
				if stripVolatile(first) != stripVolatile(second) {
					t.Fatalf("%s is not deterministic for %s input", format, fixture.name)
				}
				if fixture.jobs == nil && strings.Contains(first, "NaN") {
					t.Fatalf("%s emitted NaN for empty input", format)
				}
			})
		}
	}
}

// TestSQLInsertsDeterministicOrder pins the per-file order of the SQL
// scripts: they used to follow channel order, which worker scheduling made
// different on every run. Shuffled input must now render identical,
// Location-sorted inserts every time.
func TestSQLInsertsDeterministicOrder(t *testing.T) {
	pinLocale(t)
	resetFormatGlobals(t)
	jobs := consistencyFixture()

	for _, render := range map[string]func(chan *FileJob) string{
		"sql":        toSql,
		"sql-insert": toSqlInsert,
	} {
		first := stripVolatile(render(shuffledChan(jobs)))
		for i := 0; i < 5; i++ {
			next := stripVolatile(render(shuffledChan(jobs)))
			if next != first {
				t.Fatalf("sql output is not stable across shuffled inputs")
			}
		}

		var locs []string
		for _, m := range regexp.MustCompile(`insert into t values\('[^']*', '[^']*', '([^']+)'`).FindAllStringSubmatch(first, -1) {
			locs = append(locs, m[1])
		}
		if !slices.IsSorted(locs) {
			t.Fatalf("sql inserts are not Location-sorted: %v", locs)
		}
	}
}

// TestWriteFailureErrorNamesFormatAndPath requires the shared writer to wrap
// any failure with both the target path and the format name, so operators
// never get a bare syscall message.
func TestWriteFailureErrorNamesFormatAndPath(t *testing.T) {
	resetFormatGlobals(t)
	bad := t.TempDir() + "/does/not/exist/out.json"
	err := writeOutputFile(bad, "json", "payload")
	if err == nil {
		t.Fatal("expected a write error")
	}
	msg := err.Error()
	if !strings.Contains(msg, bad) {
		t.Errorf("error missing path %q: %s", bad, msg)
	}
	if !strings.Contains(msg, "json") {
		t.Errorf("error missing format name: %s", msg)
	}
}

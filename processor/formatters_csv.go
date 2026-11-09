// SPDX-License-Identifier: MIT

package processor

import (
	"bytes"
	"cmp"
	"encoding/csv"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

func toCSV(input chan *FileJob) string {
	if Files {
		return toCSVFiles(input)
	}

	return toCSVSummary(input)
}

func toCSVSummary(input chan *FileJob) string {
	language := aggregateLanguageSummary(input)
	language = sortLanguageSummary(language)

	record := []string{
		"Language",
		"Lines",
		"Code",
		"Comments",
		"Blanks",
		"Complexity",
		"Bytes",
		"Files",
		"ULOC",
	}
	if Cognitive {
		record = append(record, "Cognitive")
	}

	b := &bytes.Buffer{}
	w := csv.NewWriter(b)
	_ = w.Write(record)

	for _, result := range language {
		record[0] = result.Name
		record[1] = strconv.FormatInt(result.Lines, 10)
		record[2] = strconv.FormatInt(result.Code, 10)
		record[3] = strconv.FormatInt(result.Comment, 10)
		record[4] = strconv.FormatInt(result.Blank, 10)
		record[5] = strconv.FormatInt(result.Complexity, 10)
		record[6] = strconv.FormatInt(result.Bytes, 10)
		record[7] = strconv.FormatInt(result.Count, 10)
		record[8] = strconv.Itoa(len(ulocLanguageCount[result.Name]))
		if Cognitive {
			record[9] = strconv.FormatInt(result.Cognitive, 10)
		}
		_ = w.Write(record)
	}

	w.Flush()

	return b.String()
}

func getCSVFilesSortFunc(sortBy string) func(a, b []string) int {
	// Row layout: 0 Language, 1 Provider (full location), 2 Filename, 3 Lines,
	// 4 Code, 5 Comments, 6 Blanks, 7 Complexity, 8 Bytes, 9 ULOC, 10 Cognitive.
	// The normalized full location is the final tie-break in every branch so a
	// key with equal values still has one fixed order, which keeps the emitted
	// CSV byte-identical however the workers interleaved the jobs.
	byPath := func(a, b []string) int {
		return compareNormalizedPath(a[1], b[1])
	}

	// Cater for the common case of adding plural even for those options that don't make sense
	// as it's quite common for those who English is not a first language to make a simple mistake
	switch sortBy {
	case "name", "names":
		return func(a, b []string) int {
			if order := strings.Compare(a[2], b[2]); order != 0 {
				return order
			}
			return byPath(a, b)
		}
	case "language", "languages", "lang", "langs":
		return func(a, b []string) int {
			if order := strings.Compare(a[0], b[0]); order != 0 {
				return order
			}
			return byPath(a, b)
		}
	case "line", "lines":
		return func(a, b []string) int {
			i1, _ := strconv.ParseInt(a[3], 10, 64)
			i2, _ := strconv.ParseInt(b[3], 10, 64)
			if order := cmp.Compare(i2, i1); order != 0 {
				return order
			}
			return byPath(a, b)
		}
	case "blank", "blanks":
		return func(a, b []string) int {
			i1, _ := strconv.ParseInt(a[6], 10, 64)
			i2, _ := strconv.ParseInt(b[6], 10, 64)
			if order := cmp.Compare(i2, i1); order != 0 {
				return order
			}
			return byPath(a, b)
		}
	case "code", "codes":
		return func(a, b []string) int {
			i1, _ := strconv.ParseInt(a[4], 10, 64)
			i2, _ := strconv.ParseInt(b[4], 10, 64)
			if order := cmp.Compare(i2, i1); order != 0 {
				return order
			}
			return byPath(a, b)
		}
	case "comment", "comments":
		return func(a, b []string) int {
			i1, _ := strconv.ParseInt(a[5], 10, 64)
			i2, _ := strconv.ParseInt(b[5], 10, 64)
			if order := cmp.Compare(i2, i1); order != 0 {
				return order
			}
			return byPath(a, b)
		}
	case "complexity", "complexitys":
		// The cognitive value is appended as column 10 only when --cognitive is
		// set; sort by the active metric so the "complexity" key follows it.
		idx := 7
		if Cognitive {
			idx = 10
		}
		return func(a, b []string) int {
			i1, _ := strconv.ParseInt(a[idx], 10, 64)
			i2, _ := strconv.ParseInt(b[idx], 10, 64)
			if order := cmp.Compare(i2, i1); order != 0 {
				return order
			}
			return byPath(a, b)
		}
	case "cognitive", "cognitives":
		if !Cognitive {
			// No cognitive column emitted without --cognitive; fall back to name.
			return func(a, b []string) int {
				if order := strings.Compare(a[2], b[2]); order != 0 {
					return order
				}
				return byPath(a, b)
			}
		}
		return func(a, b []string) int {
			i1, _ := strconv.ParseInt(a[10], 10, 64)
			i2, _ := strconv.ParseInt(b[10], 10, 64)
			if order := cmp.Compare(i2, i1); order != 0 {
				return order
			}
			return byPath(a, b)
		}
	case "byte", "bytes":
		return func(a, b []string) int {
			i1, _ := strconv.ParseInt(a[8], 10, 64)
			i2, _ := strconv.ParseInt(b[8], 10, 64)
			if order := cmp.Compare(i2, i1); order != 0 {
				return order
			}
			return byPath(a, b)
		}
	default:
		// Default keeps the historical filename ordering and only invokes the
		// path comparison for files sharing a basename in different directories.
		return func(a, b []string) int {
			if order := strings.Compare(a[2], b[2]); order != 0 {
				return order
			}
			return byPath(a, b)
		}
	}
}

func toCSVFiles(input chan *FileJob) string {
	records := [][]string{}

	for result := range input {
		row := []string{
			result.Language,
			result.Location,
			result.Filename,
			strconv.FormatInt(result.Lines, 10),
			strconv.FormatInt(result.Code, 10),
			strconv.FormatInt(result.Comment, 10),
			strconv.FormatInt(result.Blank, 10),
			strconv.FormatInt(result.Complexity, 10),
			strconv.FormatInt(result.Bytes, 10),
			strconv.Itoa(result.Uloc),
		}
		if Cognitive {
			row = append(row, strconv.FormatInt(result.Cognitive, 10))
		}
		records = append(records, row)
	}

	slices.SortFunc(records, getCSVFilesSortFunc(SortBy))

	header := []string{
		"Language",
		"Provider",
		"Filename",
		"Lines",
		"Code",
		"Comments",
		"Blanks",
		"Complexity",
		"Bytes",
		"ULOC",
	}
	if Cognitive {
		header = append(header, "Cognitive")
	}
	recordsEnd := [][]string{header}

	recordsEnd = append(recordsEnd, records...)

	b := &bytes.Buffer{}
	w := csv.NewWriter(b)
	_ = w.WriteAll(recordsEnd)
	w.Flush()

	return b.String()
}

// For very large repositories CSV stream can be used which prints results out as they come in
// with the express idea of lowering memory usage, see https://github.com/boyter/scc/issues/210 for
// the background on why this might be needed.
//
// The rows are collected and sorted by normalized path before printing rather
// than emitted in arrival order: the workers finish in a nondeterministic
// order, so streaming arrival order straight out made two runs of the same
// tree differ byte for byte. Sorting is the only place a canonical, worker
// independent order can be imposed, and every other per-file format already
// retains its rows to do the same.
func toCSVStream(input chan *FileJob) string {
	if Cognitive {
		fmt.Println("Language,Provider,Filename,Lines,Code,Comments,Blanks,Complexity,Bytes,Uloc,Cognitive")
	} else {
		fmt.Println("Language,Provider,Filename,Lines,Code,Comments,Blanks,Complexity,Bytes,Uloc")
	}

	var results []*FileJob
	for result := range input {
		results = append(results, result)
	}
	sortFileJobsByPath(results)

	var quoteRegex = regexp.MustCompile("\"")

	for _, result := range results {
		// Escape quotes in location and filename then surround with quotes.
		var location = "\"" + quoteRegex.ReplaceAllString(result.Location, "\"\"") + "\""
		var filename = "\"" + quoteRegex.ReplaceAllString(result.Filename, "\"\"") + "\""

		if Cognitive {
			fmt.Printf("%s,%s,%s,%d,%d,%d,%d,%d,%d,%d,%d\n",
				result.Language,
				location,
				filename,
				result.Lines,
				result.Code,
				result.Comment,
				result.Blank,
				result.Complexity,
				result.Bytes,
				result.Uloc,
				result.Cognitive,
			)
		} else {
			fmt.Printf("%s,%s,%s,%d,%d,%d,%d,%d,%d,%d\n",
				result.Language,
				location,
				filename,
				result.Lines,
				result.Code,
				result.Comment,
				result.Blank,
				result.Complexity,
				result.Bytes,
				result.Uloc,
			)
		}
	}

	return ""
}

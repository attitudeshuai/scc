// SPDX-License-Identifier: MIT

package processor

import (
	"strconv"
)

// roundTo1 rounds f to one decimal place using the single rounding rule
// every output format shares. The rule is defined through the same
// conversion fmt's %.1f verb performs (round to nearest, ties to even on
// the decimal value that actually reaches the formatter once binary
// representation is accounted for), so a number carried in machine-readable
// output is always the number the tabular views print — never a neighbour a
// second, independently implemented rounding rule could pick at a tie.
//
// Formatting first and parsing back guarantees the textual and numeric
// forms agree: math.RoundToEven(f*10)/10 alone does not, because f*10 can
// land on the wrong side of a decimal tie in float64 (e.g. 1.05 stored as
// 1.05000000000000004), which made the two disagree on real counts.
func roundTo1(f float64) float64 {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	r, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return r
}

// percentRounded returns value as a percentage of total rounded with the
// shared rule, guarding total == 0 so an empty category reports 0 instead of
// NaN. It is the single percentage computation for every format: tabular
// prints its result with %.1f, JSON/json2 carry the same rounded numeric
// value.
func percentRounded(value, total int64) float64 {
	if total == 0 {
		return 0
	}
	return roundTo1(float64(value) / float64(total) * 100)
}

// formatFloat1 renders f with exactly one decimal place. It exists so callers
// share the textual form of the unified rounding rule without each holding a
// format string.
func formatFloat1(f float64) string {
	return strconv.FormatFloat(f, 'f', 1, 64)
}

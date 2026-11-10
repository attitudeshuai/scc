// SPDX-License-Identifier: MIT

package processor

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// writeChunkSize is the fixed size of the buffer every formatted output is
// copied through on its way to disk. It bounds the write stage's allocation
// peak: a result of any size moves through the same single buffer, so the
// peak does not grow with the result the way building one full-size []byte
// for os.WriteFile did.
const writeChunkSize = 64 * 1024

// writeOutputFile persists a formatted result to path, streaming it through
// a fixed writeChunkSize buffer instead of materialising one full-size
// []byte. Every output format goes through this one function so:
//
//   - the write stage's allocation peak is independent of result size;
//   - a failed write is reported the same way everywhere: the message names
//     both the target path and the format that could not be written, and the
//     wrapped error keeps the underlying reason.
//
// The string is consumed through strings.Reader without a copy, and the
// buffer is reused for every chunk, so a 16 MiB result costs the same
// allocation as a 16 KiB one.
func writeOutputFile(path, formatName string, data string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return formatWriteError(path, formatName, err)
	}

	chunk := make([]byte, writeChunkSize)
	if _, err := io.CopyBuffer(f, strings.NewReader(data), chunk); err != nil {
		_ = f.Close()
		return formatWriteError(path, formatName, err)
	}
	if err := f.Close(); err != nil {
		return formatWriteError(path, formatName, err)
	}
	return nil
}

// formatWriteError is the single error shape for every failed output write:
// "<path> unable to be written to for format <name>: <reason>".
func formatWriteError(path, formatName string, err error) error {
	return fmt.Errorf("%s unable to be written to for format %s: %w", path, formatName, err)
}

// currentFormatName is the label attached to a failed -o write. An empty
// --format means the default tabular summary, which is named explicitly so
// the error never carries an empty format slot.
func currentFormatName() string {
	if Format == "" {
		return "tabular"
	}
	return strings.ToLower(Format)
}

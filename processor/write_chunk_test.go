// SPDX-License-Identifier: MIT

// Allocation contract for the output write stage.
//
// A large result used to be handed to os.WriteFile as one full-size []byte,
// which allocated once for the string-to-bytes conversion and again inside
// WriteFile, so the write stage's allocation peak grew linearly with the
// result. writeOutputFile must instead move any size of result through one
// fixed writeChunkSize buffer. The benchmarks below give the before/after
// comparison; TestChunkedWriteAllocationsBounded turns the flat-peak
// requirement into a red/green gate.

package processor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyWholeBufferWrite reproduces the pre-fix write path and is the
// benchmark baseline the chunked writer is compared against.
func legacyWholeBufferWrite(path string, data string) error {
	return os.WriteFile(path, []byte(data), 0o644)
}

func benchPayload(b *testing.B, size int) string {
	b.Helper()
	return strings.Repeat("0123456789abcdef", size/16)
}

func benchWrite(b *testing.B, size int, write func(path, data string) error) {
	dir := b.TempDir()
	payload := benchPayload(b, size)
	path := filepath.Join(dir, "out")
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := write(path, payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteOutputFile_64KiB(b *testing.B) {
	benchWrite(b, 64*1024, func(path, data string) error {
		return writeOutputFile(path, "csv", data)
	})
}

func BenchmarkWriteOutputFile_1MiB(b *testing.B) {
	benchWrite(b, 1024*1024, func(path, data string) error {
		return writeOutputFile(path, "csv", data)
	})
}

func BenchmarkWriteOutputFile_16MiB(b *testing.B) {
	benchWrite(b, 16*1024*1024, func(path, data string) error {
		return writeOutputFile(path, "csv", data)
	})
}

// BenchmarkLegacyWholeBufferWrite_16MiB is the old path, kept so the
// benchmark comparison shows the linear peak the fix removes.
func BenchmarkLegacyWholeBufferWrite_16MiB(b *testing.B) {
	benchWrite(b, 16*1024*1024, legacyWholeBufferWrite)
}

// TestChunkedWriteAllocationsBounded runs the small and large benchmarks
// in-process and requires the bytes allocated per write to stay within two
// chunk buffers of each other. The old whole-buffer path allocates roughly
// the whole payload per call, so the 16 MiB case exceeds the bound by
// orders of magnitude before the fix.
func TestChunkedWriteAllocationsBounded(t *testing.T) {
	small := testing.Benchmark(func(b *testing.B) {
		benchWrite(b, 64*1024, func(path, data string) error {
			return writeOutputFile(path, "csv", data)
		})
	})
	large := testing.Benchmark(func(b *testing.B) {
		benchWrite(b, 16*1024*1024, func(path, data string) error {
			return writeOutputFile(path, "csv", data)
		})
	})

	t.Logf("64KiB: %d B/op, %d allocs/op; 16MiB: %d B/op, %d allocs/op",
		small.AllocedBytesPerOp(), small.AllocsPerOp(),
		large.AllocedBytesPerOp(), large.AllocsPerOp())

	bound := int64(writeChunkSize * 2)
	if delta := large.AllocedBytesPerOp() - small.AllocedBytesPerOp(); delta > bound {
		t.Fatalf("write-stage allocations grow with result size: delta=%d B, bound=%d B (peak must stay within fixed chunk buffers)", delta, bound)
	}
}

// TestChunkedWriteBeatsWholeBuffer is the explicit before/after assertion:
// for a large payload the chunked writer must allocate materially less than
// the legacy whole-buffer write (no full-payload copies).
func TestChunkedWriteBeatsWholeBuffer(t *testing.T) {
	chunked := testing.Benchmark(func(b *testing.B) {
		benchWrite(b, 16*1024*1024, func(path, data string) error {
			return writeOutputFile(path, "csv", data)
		})
	})
	legacy := testing.Benchmark(func(b *testing.B) {
		benchWrite(b, 16*1024*1024, legacyWholeBufferWrite)
	})

	t.Logf("chunked=%d B/op legacy=%d B/op", chunked.AllocedBytesPerOp(), legacy.AllocedBytesPerOp())
	if chunked.AllocedBytesPerOp() >= legacy.AllocedBytesPerOp()/2 {
		t.Fatalf("chunked write allocates %d B/op, legacy %d B/op: expected at least a 2x reduction",
			chunked.AllocedBytesPerOp(), legacy.AllocedBytesPerOp())
	}
}

// TestWriteOutputFileContentIntegrity guards the chunking: bytes on disk
// must equal the payload for sizes that cross chunk boundaries unevenly.
func TestWriteOutputFileContentIntegrity(t *testing.T) {
	for _, size := range []int{0, 1, writeChunkSize - 1, writeChunkSize, writeChunkSize + 1, 3*writeChunkSize + 7} {
		payload := strings.Repeat("x", size)
		path := filepath.Join(t.TempDir(), "out")
		if err := writeOutputFile(path, "json", payload); err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("size %d: read back: %v", size, err)
		}
		if string(got) != payload {
			t.Fatalf("size %d: content mismatch (len got=%d)", size, len(got))
		}
	}
}

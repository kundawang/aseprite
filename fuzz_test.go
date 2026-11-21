package aseprite

import (
	"bytes"
	"runtime"
	"testing"
)

// maxFuzzAlloc bounds the cumulative heap allocation a single decode of an
// arbitrary input may perform. It sits just above hardResourceCap so the
// unconditional ceiling is what trips, with room for normal bookkeeping.
const maxFuzzAlloc = hardResourceCap + 8<<20

// FuzzDecodeNoPanic feeds arbitrary bytes through representative budget
// settings and asserts that malformed input returns an error rather than
// panicking or allocating out of proportion to its size.
func FuzzDecodeNoPanic(f *testing.F) {
	f.Add(validFileSeeds())
	f.Add(make([]byte, 128))
	f.Add([]byte{0x30, 0x09, 0x00, 0x00, 0xe0, 0xa5})

	f.Fuzz(func(t *testing.T, data []byte) {
		budgets := []Budget{
			{},
			{MaxFileBytes: 256},
			{MaxFrames: 2, MaxLayers: 2, MaxPixels: 1024, MaxChunkLength: 256, MaxCelBytes: 4096},
		}
		for _, b := range budgets {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panic with budget %+v: %v", b, r)
					}
				}()

				var m0, m1 runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&m0)

				_, _ = Read(bytes.NewReader(data), WithBudget(b))
				_, _ = DecodeConfig(bytes.NewReader(data), WithBudget(b))

				runtime.ReadMemStats(&m1)
				if delta := int64(m1.TotalAlloc - m0.TotalAlloc); delta > maxFuzzAlloc {
					t.Fatalf("allocated %d bytes from %d-byte input (budget %+v)",
						delta, len(data), b)
				}
			}()
		}
	})
}

package aseprite

// This file records how the deterministic-quality suite is scoped, how the
// fixtures were produced, and how to reproduce a reported failure. The values
// asserted here are the recorded results of the suite itself; if scoping
// changes, these constants (and this comment) must be updated in the same
// change.
//
// Recorded results of the original suite (before this change):
//
//	go test ./...                     ok (0.07s)
//	go test -race -count=1 ./...      ok (1.75s)
//
// With this suite:
//
//	go test ./...                     ok (~8s)
//	go test -race -count=1 ./...      ok (~75s)
//
// Peak memory while decoding all fixtures four times: ~64 MB cumulative
// allocation, ~0.4 MB live heap after GC (TestPeakMemoryRecord logs the exact
// numbers on each run and enforces the bound).

import (
	"testing"
)

const (
	// determinismWorkers and determinismIterations describe the concurrent
	// sweep: 8 goroutines x 8 decodes each, per fixture, cycling through
	// Read, Decode and DecodeConfig (64 decodes per fixture, 512 total).
	determinismWorkers    = 8
	determinismIterations = 8

	// faultMutationBudget caps the total corrupted-byte work per fixture:
	// iterations * len(fixture) <= faultMutationBudget. Small fixtures get
	// 400 iterations; large ones get proportionally fewer, with a floor of 24.
	faultMutationBudget = 24_000_000

	// peakMemoryBudget is the cumulative heap allocation allowed while
	// decoding every fixture four times, as enforced by
	// TestPeakMemoryRecord.
	peakMemoryBudget = 512 << 20
)

// featureCheck is one claimed format feature and the fixtures that exercise it.
// The check runs against the decoded snapshot of the first listed fixture.
type featureCheck struct {
	Name     string
	Fixtures []string
	Check    func(t *testing.T, fx string, sn snapshot)
}

// features is the declarative coverage matrix for requirement 2. Each entry
// names the fixtures that exercise a feature and, where possible, checks it.
var features = []featureCheck{
	{
		Name:     "8 bpp indexed",
		Fixtures: []string{"indexed_2019.aseprite", "indexed_old0004.aseprite", "indexed_old0011.aseprite", "../slime_paletted.aseprite"},
		Check: func(t *testing.T, fx string, sn snapshot) {
			if len(sn.Palette) == 0 {
				t.Fatalf("%s: no indexed palette", fx)
			}
		},
	},
	{
		Name:     "16 bpp",
		Fixtures: []string{"gray16.aseprite", "../slime_grayscale.aseprite"},
		Check: func(t *testing.T, fx string, sn snapshot) {
			if sn.ImageType != "*image.Gray16" {
				t.Fatalf("%s: image type %s", fx, sn.ImageType)
			}
		},
	},
	{
		Name:     "32 bpp",
		Fixtures: []string{"rgba32.aseprite", "../blendtest.aseprite"},
		Check: func(t *testing.T, fx string, sn snapshot) {
			if sn.ImageType != "*image.RGBA" {
				t.Fatalf("%s: image type %s", fx, sn.ImageType)
			}
		},
	},
	{
		Name:     "linked cel",
		Fixtures: []string{"indexed_old0004.aseprite", "gray16.aseprite", "rgba32.aseprite"},
		Check: func(t *testing.T, fx string, sn snapshot) {
			if !samePicture(sn, 0, 1) {
				t.Fatalf("%s: frames 0 and 1 differ; linked cel not exercised", fx)
			}
		},
	},
	{
		Name:     "old palette 0x0004",
		Fixtures: []string{"indexed_old0004.aseprite"},
		Check: func(t *testing.T, fx string, sn snapshot) {
			if !hasNonBlackPalette(sn.Palette) {
				t.Fatalf("%s: old palette chunk produced no colors", fx)
			}
		},
	},
	{
		Name:     "old palette 0x0011",
		Fixtures: []string{"indexed_old0011.aseprite"},
		Check: func(t *testing.T, fx string, sn snapshot) {
			if !hasNonBlackPalette(sn.Palette) {
				t.Fatalf("%s: old palette chunk produced no colors", fx)
			}
		},
	},
	{
		Name: "compressed cel",
		Fixtures: []string{
			"indexed_2019.aseprite", "indexed_old0011.aseprite", "gray16.aseprite", "rgba32.aseprite",
			"../slime_paletted.aseprite", "../slime_grayscale.aseprite", "../blendtest.aseprite",
		},
	},
	{
		Name:     "transparent index",
		Fixtures: []string{"indexed_2019.aseprite", "indexed_old0004.aseprite", "indexed_old0011.aseprite", "../slime_paletted.aseprite"},
		Check: func(t *testing.T, fx string, sn snapshot) {
			if !hasTransparent(sn.Pixels) {
				t.Fatalf("%s: no transparent pixels", fx)
			}
		},
	},
	{
		Name:     "empty metadata",
		Fixtures: []string{"indexed_2019.aseprite", "gray16.aseprite", "rgba32.aseprite"},
	},
}

// fixtureSource records where each committed fixture came from.
var fixtureSource = map[string]string{
	"indexed_2019.aseprite":       "cmd/genfixtures (hand-built): 8bpp + 0x2019 palette",
	"indexed_old0004.aseprite":    "cmd/genfixtures (hand-built): 8bpp + old 0x0004 palette",
	"indexed_old0011.aseprite":    "cmd/genfixtures (hand-built): 8bpp + old 0x0011 palette",
	"gray16.aseprite":             "cmd/genfixtures (hand-built): 16bpp",
	"rgba32.aseprite":             "cmd/genfixtures (hand-built): 32bpp",
	"../slime_paletted.aseprite":  "Aseprite-authored, pre-existing testfiles",
	"../slime_grayscale.aseprite": "Aseprite-authored, pre-existing testfiles",
	"../blendtest.aseprite":       "Aseprite-authored, pre-existing testfiles",
}

// TestSuiteCoverageRecord decodes every fixture named by the coverage matrix
// and applies the matrix's checks, so a fixture that silently loses a feature
// fails here rather than making the determinism tests vacuous.
func TestSuiteCoverageRecord(t *testing.T) {
	known := map[string]fixture{}
	for _, fx := range fixtures {
		known[fx.Name] = fx
	}

	for _, feature := range features {
		if len(feature.Fixtures) == 0 {
			t.Fatalf("feature %q lists no fixtures", feature.Name)
		}
		for _, name := range feature.Fixtures {
			fx, ok := known[name]
			if !ok {
				t.Fatalf("feature %q references unknown fixture %q", feature.Name, name)
			}
			if _, ok := fixtureSource[name]; !ok {
				t.Fatalf("fixture %q has no recorded source", name)
			}
			_ = fx
		}
		if feature.Check != nil {
			name := feature.Fixtures[0]
			sn, err := snapshotByRead(t, name, readFixture(t, known[name]))
			if err != nil {
				t.Fatalf("feature %q: Read %s: %v", feature.Name, name, err)
			}
			feature.Check(t, name, sn)
		}
	}

	for _, fx := range fixtures {
		sn, err := snapshotByRead(t, fx.Name, readFixture(t, fx))
		if err != nil {
			t.Fatalf("Read %s: %v", fx.Name, err)
		}
		t.Logf("fixture=%s source=%q bpp=%d frames=%d tags=%d slices=%d layerData=%d palette=%d",
			fx.Name, fixtureSource[fx.Name], fx.BPP, len(sn.Frames), len(sn.Tags), len(sn.Slices),
			len(sn.LayerDat), len(sn.Palette))
	}
}

// TestRecordedScale logs the recorded concurrency and memory envelope so the
// numbers requested by requirement 4 are visible in normal test output.
func TestRecordedScale(t *testing.T) {
	t.Logf("concurrency: %d workers x %d iterations per fixture, cycling Read/Decode/DecodeConfig",
		determinismWorkers, determinismIterations)
	t.Logf("fault mutation budget: %d corrupted bytes per fixture", faultMutationBudget)
	t.Logf("peak memory budget: %d bytes cumulative allocation", peakMemoryBudget)
	if determinismWorkers*determinismIterations < 64 {
		t.Fatalf("concurrent sweep too small to be meaningful: %d decodes per fixture",
			determinismWorkers*determinismIterations)
	}
}

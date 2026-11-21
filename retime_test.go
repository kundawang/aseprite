package aseprite

import (
	"errors"
	"image"
	"math"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/askeladdk/aseprite/internal/require"
)

// ---- fixtures -------------------------------------------------------------

func newSprite(ticks []int64, tags []Tag) *Aseprite {
	frames := make([]Frame, len(ticks))
	for i, t := range ticks {
		frames[i].Duration = time.Duration(t) * TickDuration
	}

	return &Aseprite{Frames: frames, Tags: tags}
}

// constant-speed fixture: every frame has the same duration.
func constantSprite() *Aseprite {
	return newSprite(
		[]int64{100, 100, 100, 100},
		[]Tag{{Name: "all", Lo: 0, Hi: 3, Repeat: 7, LoopDirection: PingPong}},
	)
}

// variable-speed fixture: mixed durations.
func variableSprite() *Aseprite {
	return newSprite(
		[]int64{70, 130, 250, 50, 300},
		[]Tag{{Name: "run", Lo: 0, Hi: 4, Repeat: 2, LoopDirection: Forward}},
	)
}

// boundary fixture: zero durations, a one-tick frame and a huge duration.
func boundarySprite() *Aseprite {
	return newSprite(
		[]int64{0, 1, 1_000_000_000, 0, 3, 0},
		[]Tag{{Name: "edge", Lo: 0, Hi: 5, Repeat: 0, LoopDirection: Reverse}},
	)
}

// overlapping tags share frame 2.
func overlappingSprite() *Aseprite {
	return newSprite(
		[]int64{100, 100, 100, 100, 100},
		[]Tag{
			{Name: "a", Lo: 0, Hi: 2, Repeat: 1, LoopDirection: Forward},
			{Name: "b", Lo: 2, Hi: 4, Repeat: 1, LoopDirection: PingPong},
		},
	)
}

// ---- independent integer-tick reference implementations ------------------

// ratioRef implements half-up scaling with pure integer arithmetic,
// using an explicit numerator/denominator instead of float math.
func ratioRef(frames []int64, num, den int64) []int64 {
	out := make([]int64, len(frames))
	for i, x := range frames {
		if x == 0 {
			continue
		}

		out[i] = (x*num + den/2) / den
	}

	return out
}

// targetRef implements largest-remainder apportionment with a selection
// search rather than the implementation's sort.
func targetRef(frames []int64, target int64) []int64 {
	var sum int64
	for _, x := range frames {
		sum += x
	}

	out := make([]int64, len(frames))
	type rem struct {
		idx int64
		r   int64
	}

	var rems []rem
	var allocated int64

	for i, x := range frames {
		prod := x * target
		floor := prod / sum
		out[i] = floor
		allocated += floor

		if r := prod - floor*sum; r > 0 {
			rems = append(rems, rem{int64(i), r})
		}
	}

	for k := int64(0); k < target-allocated; k++ {
		best := 0
		for j := 1; j < len(rems); j++ {
			if rems[j].r > rems[best].r || (rems[j].r == rems[best].r && rems[j].idx < rems[best].idx) {
				best = j
			}
		}

		out[rems[best].idx]++
		rems[best].r = -1
	}

	return out
}

// tempoRef intersects each frame's source interval with every segment
// (closed-form interval math instead of the implementation's piece walk).
// The final segment's ratio region extends to the end of the tag, so the
// map end is not a split point: a frame crossing it stays one piece and is
// rounded only once.
func tempoRef(frames []int64, segs []TempoSegment) []int64 {
	ends := make([]int64, len(segs))
	for i, s := range segs {
		ends[i] = s.Ticks
		if i > 0 {
			ends[i] += ends[i-1]
		}
	}

	last := len(segs) - 1
	out := make([]int64, len(frames))
	pos := int64(0)

	for fi, x := range frames {
		a, b := pos, pos+x
		for si, s := range segs {
			lo := int64(0)
			if si > 0 {
				lo = ends[si-1]
			}

			c, d := a, b
			if c < lo {
				c = lo
			}

			// Every segment but the last is bounded by its end.
			// The last segment runs to the end of the tag.
			if si != last && d > ends[si] {
				d = ends[si]
			}

			if d > c {
				out[fi] += int64(math.Round(float64(d-c) * s.Ratio))
			}
		}

		pos = b
	}

	return out
}

func ticksOf(durations []time.Duration) []int64 {
	out := make([]int64, len(durations))
	for i, d := range durations {
		out[i] = d.Milliseconds()
	}

	return out
}

func sumTicks(ticks []int64) (sum int64) {
	for _, t := range ticks {
		sum += t
	}

	return
}

// ---- ratio mode -----------------------------------------------------------

func TestRetimeRatioConstant(t *testing.T) {
	spr := constantSprite()

	got, err := spr.Retime(RetimeOptions{TagIndex: 0, Ratio: 1.5})
	require.NoError(t, err)

	want := ratioRef([]int64{100, 100, 100, 100}, 3, 2)
	require.True(t, len(got) == 4)

	gt := ticksOf(got)
	for i := range want {
		require.True(t, gt[i] == want[i], "frame", i, gt[i], want[i])
	}

	require.True(t, sumTicks(gt) == 600, "total", sumTicks(gt))
}

func TestRetimeRatioVariable(t *testing.T) {
	spr := variableSprite()
	src := []int64{70, 130, 250, 50, 300}

	for _, tt := range []struct {
		name     string
		ratio    float64
		num, den int64
	}{
		{"quarter", 0.25, 1, 4},
		{"double", 2, 2, 1},
		{"tenth", 0.1, 1, 10},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := spr.Retime(RetimeOptions{TagIndex: 0, Ratio: tt.ratio})
			require.NoError(t, err)

			want := ratioRef(src, tt.num, tt.den)
			gt := ticksOf(got)

			for i := range want {
				require.True(t, gt[i] == want[i], "frame", i, gt[i], want[i])
			}
		})
	}
}

func TestRetimeRatioHalfAwayFromZero(t *testing.T) {
	// 3 ticks * 1.5 = 4.5 -> 5 ticks (half away from zero).
	spr := newSprite([]int64{3}, []Tag{{Lo: 0, Hi: 0}})

	got, err := spr.Retime(RetimeOptions{TagIndex: 0, Ratio: 1.5})
	require.NoError(t, err)
	require.True(t, got[0] == 5*TickDuration, got[0])
}

// ---- target duration mode -------------------------------------------------

func TestRetimeTargetVariable(t *testing.T) {
	src := []int64{70, 130, 250, 50, 300}
	spr := newSprite(src, []Tag{{Lo: 0, Hi: 4}})

	for _, target := range []int64{1000, 101, 333, 9999} {
		got, err := spr.Retime(RetimeOptions{
			TagIndex:       0,
			TargetDuration: time.Duration(target) * TickDuration,
		})
		require.NoError(t, err)

		gt := ticksOf(got)
		require.True(t, sumTicks(gt) == target, "total must equal target", sumTicks(gt), target)

		want := targetRef(src, target)
		for i := range want {
			require.True(t, gt[i] == want[i], "frame", i, gt[i], want[i])
		}
	}
}

func TestRetimeTargetConstantExact(t *testing.T) {
	spr := constantSprite()

	got, err := spr.Retime(RetimeOptions{
		TagIndex:       0,
		TargetDuration: 400 * TickDuration,
	})
	require.NoError(t, err)

	for i, d := range got {
		require.True(t, d == 100*TickDuration, "frame", i, d)
	}
}

func TestRetimeTargetRoundsHalfUp(t *testing.T) {
	spr := newSprite([]int64{100}, []Tag{{Lo: 0, Hi: 0}})

	// 1.5 ms rounds half up to 2 ticks.
	got, err := spr.Retime(RetimeOptions{TagIndex: 0, TargetDuration: TickDuration + TickDuration/2})
	require.NoError(t, err)
	require.True(t, got[0] == 2*TickDuration, got[0])

	// 1.499.. ms rounds down to 1 tick.
	got, err = spr.Retime(RetimeOptions{TagIndex: 0, TargetDuration: TickDuration + TickDuration/2 - 1})
	require.NoError(t, err)
	require.True(t, got[0] == TickDuration, got[0])
}

// ---- tempo map mode -------------------------------------------------------

func TestRetimeTempoVariable(t *testing.T) {
	src := []int64{70, 130, 250, 50, 300}
	spr := newSprite(src, []Tag{{Lo: 0, Hi: 4}})

	segs := []TempoSegment{
		{Ticks: 100, Ratio: 2},
		{Ticks: 200, Ratio: 0.5},
		{Ticks: 100, Ratio: 1.25},
	}

	got, err := spr.Retime(RetimeOptions{TagIndex: 0, TempoMap: segs})
	require.NoError(t, err)

	want := tempoRef(src, segs)
	gt := ticksOf(got)

	for i := range want {
		require.True(t, gt[i] == want[i], "frame", i, gt[i], want[i])
	}
}

func TestRetimeTempoBoundarySplitInsideFrame(t *testing.T) {
	// A single 100-tick frame straddles a 40/60 boundary at ratios 1 and 2:
	// 40*1 + round(60*2) = 160.
	spr := newSprite([]int64{100}, []Tag{{Lo: 0, Hi: 0}})
	segs := []TempoSegment{{Ticks: 40, Ratio: 1}, {Ticks: 60, Ratio: 2}}

	got, err := spr.Retime(RetimeOptions{TagIndex: 0, TempoMap: segs})
	require.NoError(t, err)
	require.True(t, got[0] == 160*TickDuration, got[0])
}

func TestRetimeTempoRunsPastMapEnd(t *testing.T) {
	// Map covers 50 ticks; the remaining 50 ticks of frame 0 and all of
	// frame 1 run at the final ratio 3.
	src := []int64{100, 100}
	spr := newSprite(src, []Tag{{Lo: 0, Hi: 1}})
	segs := []TempoSegment{{Ticks: 50, Ratio: 1}, {Ticks: 10, Ratio: 3}}

	got, err := spr.Retime(RetimeOptions{TagIndex: 0, TempoMap: segs})
	require.NoError(t, err)

	want := tempoRef(src, segs)
	gt := ticksOf(got)

	for i := range want {
		require.True(t, gt[i] == want[i], "frame", i, gt[i], want[i])
	}

	// frame 0: round(50*1) + round(50*3) = 200; frame 1: round(100*3) = 300.
	require.True(t, gt[0] == 200 && gt[1] == 300, gt)
}

func TestRetimeTempoConstantEqualsRatio(t *testing.T) {
	src := []int64{70, 130, 250, 50, 300}
	spr := newSprite(src, []Tag{{Lo: 0, Hi: 4}})
	segs := []TempoSegment{{Ticks: math.MaxInt64, Ratio: 1.5}}

	got, err := spr.Retime(RetimeOptions{TagIndex: 0, TempoMap: segs})
	require.NoError(t, err)

	want := ratioRef(src, 3, 2)
	gt := ticksOf(got)

	for i := range want {
		require.True(t, gt[i] == want[i], "frame", i, gt[i], want[i])
	}
}

// ---- boundary durations ---------------------------------------------------

func TestRetimeBoundaryZeroDurations(t *testing.T) {
	src := []int64{0, 1, 1_000_000_000, 0, 3, 0}
	spr := boundarySprite()

	t.Run("ratio keeps zeros", func(t *testing.T) {
		got, err := spr.Retime(RetimeOptions{TagIndex: 0, Ratio: 2})
		require.NoError(t, err)

		want := ratioRef(src, 2, 1)
		gt := ticksOf(got)

		for i := range want {
			require.True(t, gt[i] == want[i], "frame", i, gt[i], want[i])
		}

		require.True(t, gt[0] == 0 && gt[3] == 0 && gt[5] == 0, "zeros must stay zero")
	})

	t.Run("target keeps zeros and exact total", func(t *testing.T) {
		const target int64 = 5_000_000
		got, err := spr.Retime(RetimeOptions{
			TagIndex:       0,
			TargetDuration: time.Duration(target) * TickDuration,
		})
		require.NoError(t, err)

		gt := ticksOf(got)
		require.True(t, sumTicks(gt) == target, "total", sumTicks(gt))
		require.True(t, gt[0] == 0 && gt[3] == 0 && gt[5] == 0, "zero-weight frames stay zero")

		want := targetRef(src, target)
		for i := range want {
			require.True(t, gt[i] == want[i], "frame", i, gt[i], want[i])
		}
	})

	t.Run("all-zero tag cannot take a target", func(t *testing.T) {
		zero := newSprite([]int64{0, 0, 0}, []Tag{{Lo: 0, Hi: 2}})
		_, err := zero.Retime(RetimeOptions{TagIndex: 0, TargetDuration: 10 * TickDuration})
		require.True(t, errors.Is(err, ErrAllZeroDurations), err)
	})
}

func TestRetimeBoundaryHugeDuration(t *testing.T) {
	// Scaling the largest representable duration by 2 overflows time.Duration.
	spr := newSprite([]int64{math.MaxInt64 / int64(TickDuration)}, []Tag{{Lo: 0, Hi: 0}})

	_, err := spr.Retime(RetimeOptions{TagIndex: 0, Ratio: 2})
	require.True(t, errors.Is(err, ErrDurationOverflow), err)

	// A float ratio that produces more than MaxInt64 ticks overflows early.
	spr2 := newSprite([]int64{1}, []Tag{{Lo: 0, Hi: 0}})
	_, err = spr2.Retime(RetimeOptions{TagIndex: 0, Ratio: 1e20})
	require.True(t, errors.Is(err, ErrDurationOverflow), err)
}

func TestRetimeTargetOverflow(t *testing.T) {
	maxTicks := int64(math.MaxInt64 / int64(TickDuration))

	t.Run("sum overflows", func(t *testing.T) {
		// Tick-level inputs bypass the duration entry check.
		_, err := retimeTarget([]int64{math.MaxInt64, math.MaxInt64}, TickDuration)
		require.True(t, errors.Is(err, ErrDurationOverflow), err)
	})

	t.Run("product overflows", func(t *testing.T) {
		spr := newSprite([]int64{maxTicks}, []Tag{{Lo: 0, Hi: 0}})
		_, err := spr.Retime(RetimeOptions{
			TagIndex:       0,
			TargetDuration: time.Duration(maxTicks) * TickDuration,
		})
		require.True(t, errors.Is(err, ErrDurationOverflow), err)
	})
}

func TestRetimeTempoOverflow(t *testing.T) {
	spr := newSprite([]int64{1}, []Tag{{Lo: 0, Hi: 0}})

	// Segment lengths that overflow when summed.
	_, err := spr.Retime(RetimeOptions{
		TagIndex: 0,
		TempoMap: []TempoSegment{
			{Ticks: math.MaxInt64, Ratio: 1},
			{Ticks: math.MaxInt64, Ratio: 1},
		},
	})
	require.True(t, errors.Is(err, ErrDurationOverflow), err)
}

func TestRetimeTempoEmptyMapIsRejected(t *testing.T) {
	// Direct tick-level call: the public mode counter never reaches here
	// with a nil map, but the guard must still be sound.
	_, err := retimeTempo([]int64{1}, nil)
	require.True(t, errors.Is(err, ErrInvalidTempoMap), err)
}

// TestRetimeRandomizedCrossCheck fuzzes frame durations against the three
// independent integer-tick reference implementations, including zeros.
func TestRetimeRandomizedCrossCheck(t *testing.T) {
	seed := time.Now().UnixNano()
	rng := rand.New(rand.NewSource(seed))

	for iter := 0; iter < 200; iter++ {
		n := 1 + rng.Intn(8)
		src := make([]int64, n)
		allZero := true

		for i := range src {
			// Sprinkle zero durations in with a 1/4 probability.
			if rng.Intn(4) == 0 {
				src[i] = 0
				continue
			}

			allZero = false
			src[i] = 1 + rng.Int63n(500)
		}

		if allZero {
			src[rng.Intn(n)] = 1 + rng.Int63n(500)
		}

		spr := newSprite(src, []Tag{{Lo: 0, Hi: uint16(n - 1)}})

		// Ratio mode against the rational integer reference (3/7).
		got, err := spr.Retime(RetimeOptions{TagIndex: 0, Ratio: 3.0 / 7.0})
		require.NoError(t, err)

		want := ratioRef(src, 3, 7)
		gt := ticksOf(got)

		for i := range want {
			require.True(t, gt[i] == want[i], "ratio mismatch", "seed", seed, "iter", iter, "frame", i)
		}

		// Target mode against the largest-remainder reference.
		target := 1 + rng.Int63n(3000)
		got, err = spr.Retime(RetimeOptions{
			TagIndex:       0,
			TargetDuration: time.Duration(target) * TickDuration,
		})
		require.NoError(t, err)

		require.True(t, sumTicks(ticksOf(got)) == target, "target total", "seed", seed, "iter", iter)

		want = targetRef(src, target)
		gt = ticksOf(got)

		for i := range want {
			require.True(t, gt[i] == want[i], "target mismatch", "seed", seed, "iter", iter, "frame", i)
		}

		// Tempo mode against the interval-intersection reference.
		segs := []TempoSegment{
			{Ticks: 1 + rng.Int63n(200), Ratio: 0.5 + rng.Float64()*2},
			{Ticks: 1 + rng.Int63n(200), Ratio: 0.5 + rng.Float64()*2},
			{Ticks: 1 + rng.Int63n(200), Ratio: 0.5 + rng.Float64()*2},
		}

		got, err = spr.Retime(RetimeOptions{TagIndex: 0, TempoMap: segs})
		require.NoError(t, err)

		want = tempoRef(src, segs)
		gt = ticksOf(got)

		for i := range want {
			require.True(t, gt[i] == want[i], "tempo mismatch", "seed", seed, "iter", iter, "frame", i)
		}
	}
}

// ---- tags, overlap and rollback -------------------------------------------

func TestRetimePreservesTagMetadata(t *testing.T) {
	spr := constantSprite()
	before := spr.Tags[0]

	require.NoError(t, spr.RetimeInPlace(RetimeOptions{TagIndex: 0, Ratio: 3}))

	after := spr.Tags[0]
	require.True(t, after == before, "tag changed", after, before)
}

func TestRetimeOverlappingTagsAgree(t *testing.T) {
	spr := overlappingSprite()

	// Both tags apply the same ratio, so shared frame 2 computes 200 in both.
	got, err := spr.Retime(
		RetimeOptions{TagIndex: 0, Ratio: 2},
		RetimeOptions{TagIndex: 1, Ratio: 2},
	)
	require.NoError(t, err)

	gt := ticksOf(got)
	for i, want := range []int64{200, 200, 200, 200, 200} {
		require.True(t, gt[i] == want, "frame", i, gt[i], want)
	}
}

func TestRetimeOverlappingTagsDisagree(t *testing.T) {
	spr := overlappingSprite()

	_, err := spr.Retime(
		RetimeOptions{TagIndex: 0, Ratio: 2},
		RetimeOptions{TagIndex: 1, Ratio: 3},
	)
	require.True(t, errors.Is(err, ErrOverlappingTags), err)

	// No frames were touched.
	for i, fr := range spr.Frames {
		require.True(t, fr.Duration == 100*TickDuration, "frame mutated", i, fr.Duration)
	}
}

func TestRetimeDoesNotMutateOnCopy(t *testing.T) {
	spr := variableSprite()
	before := append([]time.Duration(nil), durationsOf(spr)...)

	got, err := spr.Retime(RetimeOptions{TagIndex: 0, Ratio: 2})
	require.NoError(t, err)
	require.True(t, &got[0] != &spr.Frames[0].Duration, "result must not alias sprite frames")

	for i := range before {
		require.True(t, spr.Frames[i].Duration == before[i], "sprite mutated", i)
	}
}

func TestRetimeInPlaceRollbackOnError(t *testing.T) {
	spr := overlappingSprite()
	before := append([]time.Duration(nil), durationsOf(spr)...)

	err := spr.RetimeInPlace(
		RetimeOptions{TagIndex: 0, Ratio: 2},
		RetimeOptions{TagIndex: 1, Ratio: 3},
	)
	require.True(t, errors.Is(err, ErrOverlappingTags), err)

	for i := range before {
		require.True(t, spr.Frames[i].Duration == before[i], "frame changed after failed retime", i)
	}
}

func TestRetimeUncoveredFramesKeepDuration(t *testing.T) {
	spr := newSprite(
		[]int64{100, 100, 100, 100, 100},
		[]Tag{{Name: "head", Lo: 0, Hi: 1}},
	)

	got, err := spr.Retime(RetimeOptions{TagIndex: 0, Ratio: 2})
	require.NoError(t, err)

	gt := ticksOf(got)
	require.True(t, gt[0] == 200 && gt[1] == 200, "tag frames retimed")
	require.True(t, gt[2] == 100 && gt[3] == 100 && gt[4] == 100, "uncovered frames untouched")
}

func TestRetimeNoOptionsReturnsFullCopy(t *testing.T) {
	spr := variableSprite()

	got, err := spr.Retime()
	require.NoError(t, err)
	require.True(t, len(got) == len(spr.Frames))

	for i := range got {
		require.True(t, got[i] == spr.Frames[i].Duration, "frame", i)
	}
}

// ---- input validation -----------------------------------------------------

func TestRetimeValidationErrors(t *testing.T) {
	spr := variableSprite()

	for _, tt := range []struct {
		name string
		opt  RetimeOptions
		want error
	}{
		{"tag index negative", RetimeOptions{TagIndex: -1, Ratio: 1}, ErrInvalidTagIndex},
		{"tag index too large", RetimeOptions{TagIndex: 99, Ratio: 1}, ErrInvalidTagIndex},
		{"zero ratio", RetimeOptions{TagIndex: 0, Ratio: 0}, ErrRetimeMode},
		{"negative ratio", RetimeOptions{TagIndex: 0, Ratio: -1}, ErrInvalidRatio},
		{"nan ratio", RetimeOptions{TagIndex: 0, Ratio: math.NaN()}, ErrInvalidRatio},
		{"inf ratio", RetimeOptions{TagIndex: 0, Ratio: math.Inf(1)}, ErrInvalidRatio},
		{"negative target", RetimeOptions{TagIndex: 0, TargetDuration: -TickDuration}, ErrInvalidDuration},
		{"sub-tick target rounds to zero", RetimeOptions{TagIndex: 0, TargetDuration: time.Nanosecond}, ErrInvalidDuration},
		{"empty tempo map", RetimeOptions{TagIndex: 0, TempoMap: nil}, ErrRetimeMode},
		{
			"zero-length segment",
			RetimeOptions{TagIndex: 0, TempoMap: []TempoSegment{{Ticks: 0, Ratio: 1}}},
			ErrInvalidTempoMap,
		},
		{
			"bad segment ratio",
			RetimeOptions{TagIndex: 0, TempoMap: []TempoSegment{{Ticks: 10, Ratio: 0}}},
			ErrInvalidTempoMap,
		},
		{"two modes", RetimeOptions{TagIndex: 0, Ratio: 1, TargetDuration: TickDuration}, ErrRetimeMode},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := spr.Retime(tt.opt)
			require.True(t, errors.Is(err, tt.want), "want", tt.want, "got", err)
		})
	}
}

func TestRetimeRejectsReversedTagRange(t *testing.T) {
	spr := newSprite([]int64{10, 10}, []Tag{{Lo: 1, Hi: 0}})
	_, err := spr.Retime(RetimeOptions{TagIndex: 0, Ratio: 1})
	require.True(t, errors.Is(err, ErrInvalidTagIndex), err)
}

func TestRetimeRejectsNegativeFrameDuration(t *testing.T) {
	spr := newSprite(nil, []Tag{{Lo: 0, Hi: 0}})
	spr.Frames = []Frame{{Duration: -TickDuration}}

	_, err := spr.Retime(RetimeOptions{TagIndex: 0, Ratio: 1})
	require.True(t, errors.Is(err, ErrInvalidDuration), err)
}

// ---- real file integration ------------------------------------------------

func TestRetimeOnDecodedFile(t *testing.T) {
	f, err := os.Open("./testfiles/slime_paletted.aseprite")
	require.NoError(t, err)
	defer f.Close()

	spr, err := Read(f)
	require.NoError(t, err)

	originalTags := append([]Tag(nil), spr.Tags...)
	original := append([]time.Duration(nil), durationsOf(spr)...)
	originalBounds := append([]image.Rectangle(nil), boundsOf(spr)...)

	// Up tag (frames 0-3) at half speed, doubling durations;
	// Down tag (frames 5-9) compressed to 500ms.
	err = spr.RetimeInPlace(
		RetimeOptions{TagIndex: 0, Ratio: 2},
		RetimeOptions{TagIndex: 1, TargetDuration: 500 * TickDuration},
	)
	require.NoError(t, err)

	gt := ticksOf(durationsOf(spr))

	// Up: 100ms frames become 200ms.
	for i := 0; i <= 3; i++ {
		require.True(t, gt[i] == 200, "up frame", i, gt[i])
	}

	// Frame 4 belongs to no tag and is untouched.
	require.True(t, gt[4] == 100, "untagged frame", gt[4])

	// Down totals exactly 500ms and matches the integer reference.
	down := gt[5:]
	require.True(t, sumTicks(down) == 500, "down total", sumTicks(down))

	wantDown := targetRef([]int64{100, 100, 100, 100, 100}, 500)
	for i := range wantDown {
		require.True(t, down[i] == wantDown[i], "down frame", i, down[i], wantDown[i])
	}

	// Tag metadata is untouched.
	for i := range originalTags {
		require.True(t, spr.Tags[i] == originalTags[i], "tag", i)
	}

	// All frames still accounted for and other fields survive.
	require.True(t, len(spr.Frames) == len(original))
	for i, b := range originalBounds {
		require.True(t, spr.Frames[i].Bounds == b, "bounds changed", i)
	}
}

func durationsOf(spr *Aseprite) []time.Duration {
	out := make([]time.Duration, len(spr.Frames))
	for i, fr := range spr.Frames {
		out[i] = fr.Duration
	}

	return out
}

func boundsOf(spr *Aseprite) []image.Rectangle {
	out := make([]image.Rectangle, len(spr.Frames))
	for i, fr := range spr.Frames {
		out[i] = fr.Bounds
	}

	return out
}

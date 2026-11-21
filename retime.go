package aseprite

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// TickDuration is the time resolution of a retimed timeline.
// Aseprite stores frame durations as integer milliseconds,
// so every retimed duration is rounded to whole TickDuration ticks.
const TickDuration = time.Millisecond

var (
	// ErrInvalidTagIndex is returned when RetimeOptions.TagIndex does not
	// refer to an existing tag, or the tag's frame range is out of bounds.
	ErrInvalidTagIndex = errors.New("aseprite: retime tag index out of range")

	// ErrInvalidDuration is returned when a target duration is zero or
	// negative, a target duration rounds to zero ticks, or a frame duration
	// is negative.
	ErrInvalidDuration = errors.New("aseprite: invalid retime duration")

	// ErrInvalidRatio is returned when a scaling ratio is zero, negative,
	// NaN or infinite.
	ErrInvalidRatio = errors.New("aseprite: retime ratio must be positive and finite")

	// ErrInvalidTempoMap is returned when a tempo map is empty, has a
	// segment with non-positive Ticks, or a segment Ratio that is zero,
	// negative, NaN or infinite.
	ErrInvalidTempoMap = errors.New("aseprite: invalid retime tempo map")

	// ErrAllZeroDurations is returned when a target total duration is
	// requested for a tag whose frames all have zero duration: there is no
	// weighting to distribute the target over.
	ErrAllZeroDurations = errors.New("aseprite: cannot retime a tag whose frames all have zero duration")

	// ErrOverlappingTags is returned when two retimed tags share a frame
	// and compute different durations for it. No frame is modified when
	// this happens. Identical durations computed for a shared frame are
	// accepted.
	ErrOverlappingTags = errors.New("aseprite: retimed tags overlap and disagree on a shared frame")

	// ErrDurationOverflow is returned when a retimed duration does not fit
	// in an int64 number of ticks or a time.Duration.
	ErrDurationOverflow = errors.New("aseprite: retimed duration overflows")

	// ErrRetimeMode is returned when none or more than one of Ratio,
	// TargetDuration and TempoMap is set.
	ErrRetimeMode = errors.New("aseprite: exactly one of Ratio, TargetDuration and TempoMap must be set")
)

// TempoSegment describes one constant-tempo stretch of a tempo map.
// Ticks source ticks are played back at Ratio, so a segment contributes
// Ticks*Ratio output ticks.
type TempoSegment struct {
	// Ticks is the length of the segment on the source timeline, in ticks.
	Ticks int64

	// Ratio is the playback ratio applied to the segment.
	// It must be positive and finite.
	Ratio float64
}

// RetimeOptions describes a single tag timeline retiming operation.
// Exactly one of Ratio, TargetDuration and TempoMap must be set.
type RetimeOptions struct {
	// TagIndex selects the tag in Aseprite.Tags to retime.
	// Only the durations of the frames in the tag's [Lo, Hi] range change.
	TagIndex int

	// Ratio scales every frame duration in the tag by Ratio,
	// rounding half away from zero to whole ticks. Zero-duration frames
	// stay zero.
	Ratio float64

	// TargetDuration rescales the tag so that the sum of its frame
	// durations equals TargetDuration rounded half up to whole ticks.
	// Each frame keeps its relative weight.
	TargetDuration time.Duration

	// TempoMap retimes the tag through a sequence of constant-tempo
	// segments laid out along the tag's own tick timeline. A frame that
	// crosses a boundary where the ratio changes is split into pieces,
	// each piece rounded half away from zero and the pieces summed. The
	// final segment's ratio keeps applying past the end of the map; that
	// end is not a split point, so a frame running into it is rounded once.
	TempoMap []TempoSegment
}

// Retime computes retimed frame durations for the selected tags and
// returns a copy of the full frame duration slice. The sprite is never
// modified: pass the result to a copy, or use RetimeInPlace to apply it.
//
// Frames that no selected tag covers keep their original durations.
// Tag ranges, Repeat values and loop directions are never modified.
// If any option is invalid then an error is returned and no result is
// computed; errors.Is can be used to match the exported Err* sentinels.
func (spr *Aseprite) Retime(opts ...RetimeOptions) ([]time.Duration, error) {
	nframes := len(spr.Frames)

	ticks := make([]int64, nframes)
	for i, fr := range spr.Frames {
		t, err := durationToTicks(fr.Duration)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", i, err)
		}
		ticks[i] = t
	}

	// claimed[i] is true once a retimed tag has written frame i.
	claimed := make([]bool, nframes)
	out := append([]int64(nil), ticks...)

	for oi, opt := range opts {
		lo, hi, err := spr.tagBounds(opt.TagIndex, nframes)
		if err != nil {
			return nil, fmt.Errorf("retime option %d: %w", oi, err)
		}

		res, err := retimeTag(ticks[lo:hi+1], opt)
		if err != nil {
			return nil, fmt.Errorf("retime option %d (tag %q): %w", oi, spr.Tags[opt.TagIndex].Name, err)
		}

		for k, v := range res {
			i := lo + k
			if claimed[i] && out[i] != v {
				return nil, fmt.Errorf("frame %d: %w", i, ErrOverlappingTags)
			}
			out[i] = v
			claimed[i] = true
		}
	}

	durations := make([]time.Duration, nframes)
	for i, t := range out {
		d, err := ticksToDuration(t)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", i, err)
		}
		durations[i] = d
	}

	return durations, nil
}

// RetimeInPlace applies retimed frame durations to the sprite.
// All options are validated and computed before any frame is touched,
// so an invalid input leaves the sprite completely unchanged.
func (spr *Aseprite) RetimeInPlace(opts ...RetimeOptions) error {
	durations, err := spr.Retime(opts...)
	if err != nil {
		return err
	}

	for i := range spr.Frames {
		spr.Frames[i].Duration = durations[i]
	}

	return nil
}

func (spr *Aseprite) tagBounds(tagIndex, nframes int) (int, int, error) {
	if tagIndex < 0 || tagIndex >= len(spr.Tags) {
		return 0, 0, ErrInvalidTagIndex
	}

	tag := spr.Tags[tagIndex]
	lo, hi := int(tag.Lo), int(tag.Hi)

	if lo > hi || hi >= nframes {
		return 0, 0, ErrInvalidTagIndex
	}

	return lo, hi, nil
}

// retimeTag computes the retimed tick count for every frame of one tag.
// frames is the slice of original tick counts covering [Lo, Hi].
func retimeTag(frames []int64, opt RetimeOptions) ([]int64, error) {
	modes := 0
	if opt.Ratio != 0 {
		modes++
	}
	if opt.TargetDuration != 0 {
		modes++
	}
	if opt.TempoMap != nil {
		modes++
	}

	if modes != 1 {
		return nil, ErrRetimeMode
	}

	switch {
	case opt.Ratio != 0:
		return retimeRatio(frames, opt.Ratio)
	case opt.TargetDuration != 0:
		return retimeTarget(frames, opt.TargetDuration)
	default:
		return retimeTempo(frames, opt.TempoMap)
	}
}

// retimeRatio scales each frame independently. Zero frames stay zero and
// every other value is rounded half away from zero.
func retimeRatio(frames []int64, ratio float64) ([]int64, error) {
	// NaN, negative, zero and +Inf all fail the positive-finite test.
	if !(ratio > 0) || math.IsInf(ratio, 0) {
		return nil, ErrInvalidRatio
	}

	out := make([]int64, len(frames))

	for i, x := range frames {
		if x == 0 {
			continue
		}

		v, err := scaleTicks(x, ratio)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", i, err)
		}

		out[i] = v
	}

	return out, nil
}

// retimeTarget fits the tag's frames to target ticks. Each frame receives
// floor(weight*target/sum); the leftover ticks from integer division are
// handed out one each to the frames with the largest remainders, ties
// broken by ascending frame index. Zero-duration frames keep their zero
// weight and never receive leftover ticks.
func retimeTarget(frames []int64, targetDuration time.Duration) ([]int64, error) {
	target, err := durationToTicks(targetDuration)
	if err != nil {
		return nil, err
	} else if target <= 0 {
		return nil, fmt.Errorf("%w: target rounds to zero ticks", ErrInvalidDuration)
	}

	var sum int64
	for _, x := range frames {
		sum, err = addOverflow(sum, x)
		if err != nil {
			return nil, err
		}
	}

	if sum == 0 {
		return nil, ErrAllZeroDurations
	}

	out := make([]int64, len(frames))
	type remainder struct {
		idx int
		rem int64
	}

	entries := make([]remainder, 0, len(frames))
	var allocated int64

	for i, x := range frames {
		prod, err := multiplyOverflow(x, target)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", i, err)
		}

		floor := prod / sum
		out[i] = floor

		allocated, err = addOverflow(allocated, floor)
		if err != nil {
			return nil, err
		}

		if rem := prod - floor*sum; rem > 0 {
			entries = append(entries, remainder{i, rem})
		}
	}

	leftover := target - allocated

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].rem != entries[j].rem {
			return entries[i].rem > entries[j].rem
		}
		return entries[i].idx < entries[j].idx
	})

	for k := int64(0); k < leftover; k++ {
		out[entries[k].idx]++
	}

	return out, nil
}

// retimeTempo walks the tag's frames through the tempo map. A frame that
// crosses a ratio-change boundary is split into pieces, each rounded half
// away from zero, and the pieces are summed. The final segment is
// unbounded: past the map end its ratio keeps applying as a single piece,
// so crossing the map end never causes an extra rounding.
func retimeTempo(frames []int64, segments []TempoSegment) ([]int64, error) {
	if len(segments) == 0 {
		return nil, fmt.Errorf("%w: empty", ErrInvalidTempoMap)
	}

	var mapLength int64
	for i, seg := range segments {
		if seg.Ticks <= 0 {
			return nil, fmt.Errorf("%w: segment %d has non-positive length", ErrInvalidTempoMap, i)
		}

		if !(seg.Ratio > 0) || math.IsInf(seg.Ratio, 0) {
			return nil, fmt.Errorf("%w: segment %d ratio must be positive and finite", ErrInvalidTempoMap, i)
		}

		var err error
		mapLength, err = addOverflow(mapLength, seg.Ticks)
		if err != nil {
			return nil, fmt.Errorf("%w: segment %d", ErrDurationOverflow, i)
		}
	}

	out := make([]int64, len(frames))
	si := 0
	segEnd := segments[0].Ticks
	pos := int64(0)

	for i, x := range frames {
		remaining := x

		for remaining > 0 {
			for si < len(segments)-1 && pos >= segEnd {
				next, err := addOverflow(segEnd, segments[si+1].Ticks)
				if err != nil {
					return nil, fmt.Errorf("frame %d: %w", i, ErrDurationOverflow)
				}

				segEnd = next
				si++
			}

			take := remaining
			if si < len(segments)-1 {
				if cap := segEnd - pos; cap < take {
					take = cap
				}
			}

			v, err := scaleTicks(take, segments[si].Ratio)
			if err != nil {
				return nil, fmt.Errorf("frame %d: %w", i, err)
			}

			out[i], err = addOverflow(out[i], v)
			if err != nil {
				return nil, fmt.Errorf("frame %d: %w", i, err)
			}

			pos += take
			remaining -= take
		}
	}

	return out, nil
}

// durationToTicks truncates to whole ticks and then rounds half up.
// Inputs are non-negative, so half up equals half away from zero.
func durationToTicks(d time.Duration) (int64, error) {
	if d < 0 {
		return 0, fmt.Errorf("%w: negative duration", ErrInvalidDuration)
	}

	tick := int64(TickDuration)
	q := int64(d / TickDuration)
	rem := int64(d) - q*tick

	if rem*2 >= tick {
		q++
	}

	return q, nil
}

func ticksToDuration(ticks int64) (time.Duration, error) {
	tick := int64(TickDuration)

	if ticks > math.MaxInt64/tick {
		return 0, ErrDurationOverflow
	}

	return time.Duration(ticks * tick), nil
}

func scaleTicks(x int64, ratio float64) (int64, error) {
	y := math.Round(float64(x) * ratio)
	if y < 0 || math.IsNaN(y) || y > float64(math.MaxInt64) {
		return 0, ErrDurationOverflow
	}

	return int64(y), nil
}

func addOverflow(a, b int64) (int64, error) {
	if b > math.MaxInt64-a {
		return 0, ErrDurationOverflow
	}

	return a + b, nil
}

func multiplyOverflow(a, b int64) (int64, error) {
	if a != 0 && b > math.MaxInt64/a {
		return 0, ErrDurationOverflow
	}

	return a * b, nil
}

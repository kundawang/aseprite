package aseprite

import (
	"errors"
	"math"
	"sort"
	"time"
)

// Errors returned by the timeline iterator.
var (
	errNilTag           = errors.New("aseprite: nil tag")
	errEmptyTag         = errors.New("aseprite: empty tag: Lo is greater than Hi")
	errTagOutOfBounds   = errors.New("aseprite: tag frame range is out of bounds")
	errZeroRepeat       = errors.New("aseprite: tag repeat count is zero")
	errInvalidDirection = errors.New("aseprite: invalid tag loop direction")
	errNegativeDuration = errors.New("aseprite: frame duration is negative")
	errDurationOverflow = errors.New("aseprite: animation duration overflows time.Duration")
	errTimelineEnded    = errors.New("aseprite: timeline has ended")
	errInvalidTime      = errors.New("aseprite: time must not be negative")
)

// Sample is a single point on a tag's timeline.
//
// A sample describes that Frame is displayed during the half-open interval
// [Time, Time+Frame.Duration). Two consecutive samples never share a time
// stamp: zero-duration frames produce no samples.
type Sample struct {
	// Time is the time at which the frame starts displaying, measured
	// from the beginning of the timeline.
	Time time.Duration

	// Frame is the absolute index of the frame in Aseprite.Frames,
	// not the index relative to the tag.
	Frame int

	// Loop is the zero-based loop count in which the sample occurs.
	Loop int
}

// Timeline iterates the frames of an animation Tag over time.
//
// A timeline is built from a Tag and the sprite's Frames and is valid for the
// lifetime of the frame slice; the frames themselves are only read, never
// modified. The Tag is copied on construction and the original is left
// untouched.
//
// The timeline is modeled as a sequence of half-open display intervals
// [start, start+duration). A frame with a zero duration occupies no interval
// and produces no sample. The instant Total is not part of the timeline:
// requesting it reports errTimelineEnded.
//
// PingPong animations are modeled as passes. Every loop consists of a forward
// pass (Lo to Hi) and a reverse pass (Hi to Lo), or the other way around for
// PingPongReverse. The boundary instant between two passes belongs to the
// later pass, so the endpoint frames are displayed exactly once per pass and
// never duplicated on the boundary.
//
// Timeline is not safe for concurrent use.
type Timeline struct {
	tag    Tag
	frames []Frame

	passes []pass
	n      int
	time   time.Duration
	total  time.Duration

	pi   int // current pass index
	li   int // current frame index within the pass
	done bool
}

// pass is one traversal over every frame in the tag range.
type pass struct {
	start time.Duration // global time at which the pass starts
	loop  int           // zero-based loop count the pass belongs to
	desc  bool          // true traverses Hi to Lo, false Lo to Hi
}

// NewTimeline builds a Timeline from tag and the sprite's frames.
//
// The tag is copied and never modified. An error is returned and the input tag
// is left untouched when the tag is nil, its frame range is empty or out of
// bounds, Repeat is zero, the loop direction is unknown, a frame has a
// negative duration, or the accumulated animation duration overflows
// time.Duration.
//
// Aseprite stores Repeat zero to mean infinite playback. Infinite timelines
// cannot be sampled at an arbitrary time and are rejected up front; set a
// finite Repeat on the tag copy before constructing a timeline.
func NewTimeline(tag *Tag, frames []Frame) (*Timeline, error) {
	if tag == nil {
		return nil, errNilTag
	}

	if tag.Lo > tag.Hi {
		return nil, errEmptyTag
	}

	if tag.Lo < 0 || tag.Hi >= len(frames) {
		return nil, errTagOutOfBounds
	}

	if tag.Repeat == 0 {
		return nil, errZeroRepeat
	}

	switch tag.LoopDirection() {
	case Forward, Reverse, PingPong, PingPongReverse:
	default:
		return nil, errInvalidDirection
	}

	tl := &Timeline{
		tag:    *tag,
		frames: frames[tag.Lo : tag.Hi+1],
		n:      tag.Hi - tag.Lo + 1,
	}

	// Every pass displays every frame once, so the pass length is the sum
	// of all frame durations in the tag range.
	var passLen time.Duration
	for _, fr := range tl.frames {
		if fr.Duration < 0 {
			return nil, errNegativeDuration
		}

		next, ok := addDuration(passLen, fr.Duration)
		if !ok {
			return nil, errDurationOverflow
		}

		passLen = next
	}

	npasses := tag.Repeat
	if dir := tag.LoopDirection(); dir == PingPong || dir == PingPongReverse {
		npasses = 2 * tag.Repeat
	}

	// Check the total before building the passes so that construction
	// never produces a timeline whose time stamps cannot be represented.
	if passLen > 0 {
		if int64(npasses) > math.MaxInt64/int64(passLen) {
			return nil, errDurationOverflow
		}

		tl.total = time.Duration(int64(npasses) * int64(passLen))
	}

	tl.passes = make([]pass, npasses)

	var start time.Duration

	for q := range tl.passes {
		var p pass

		switch tag.LoopDirection() {
		case Forward:
			p = pass{start: start, loop: q, desc: false}
		case Reverse:
			p = pass{start: start, loop: q, desc: true}
		case PingPong:
			p = pass{start: start, loop: q / 2, desc: q%2 == 1}
		case PingPongReverse:
			p = pass{start: start, loop: q / 2, desc: q%2 == 0}
		}

		tl.passes[q] = p

		// Cannot overflow: total was validated above. The check stays
		// in place to keep the arithmetic honest regardless.
		next, ok := addDuration(start, passLen)
		if !ok {
			return nil, errDurationOverflow
		}

		start = next
	}

	return tl, nil
}

// addDuration returns a+b and reports whether the sum fits time.Duration.
// Negative b is rejected by the caller before this is used.
func addDuration(a, b time.Duration) (time.Duration, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, false
	}

	return a + b, true
}

// Next returns the sample at the current timeline position and advances the
// position to the next frame with a positive duration.
//
// Zero-duration frames are skipped. It returns errTimelineEnded once every
// loop has been consumed.
func (tl *Timeline) Next() (Sample, error) {
	if tl.done {
		return Sample{}, errTimelineEnded
	}

	for tl.pi < len(tl.passes) {
		p := &tl.passes[tl.pi]

		for tl.li < tl.n {
			k := tl.li
			tl.li++

			idx := k
			if p.desc {
				idx = tl.n - 1 - k
			}

			d := tl.frames[idx].Duration
			st := tl.time

			// The running time can never overflow: construction proved
			// that Total fits time.Duration.
			tl.time += d

			if d == 0 {
				continue
			}

			if tl.li == tl.n {
				tl.pi++
				tl.li = 0

				if tl.pi == len(tl.passes) {
					tl.done = true
				}
			}

			return Sample{
				Time:  st,
				Frame: tl.tag.Lo + idx,
				Loop:  p.loop,
			}, nil
		}

		// The pass contained only zero-duration frames. It occupies no
		// time, so move on without emitting a sample.
		tl.pi++
		tl.li = 0

		if tl.pi == len(tl.passes) {
			tl.done = true
			return Sample{}, errTimelineEnded
		}
	}

	return Sample{}, errTimelineEnded
}

// At returns the sample displayed at time t.
//
// The returned Sample.Time is the start time of the frame that contains t,
// not t itself. Display intervals are half-open: a frame displayed from s with
// duration d matches s <= t < s+d, so pass and loop boundaries resolve to the
// frame starting at the boundary, never to the frame that just ended.
//
// It returns errInvalidTime for a negative t and errTimelineEnded when t is at
// or beyond Total. A timeline made entirely of zero-duration frames has a
// zero Total and reports errTimelineEnded for every non-negative t.
func (tl *Timeline) At(t time.Duration) (Sample, error) {
	if t < 0 {
		return Sample{}, errInvalidTime
	}

	if tl.total == 0 || t >= tl.total {
		return Sample{}, errTimelineEnded
	}

	// Largest pass whose start does not exceed t. Pass starts are
	// strictly increasing whenever Total is positive.
	q := sort.Search(len(tl.passes), func(i int) bool {
		return tl.passes[i].start > t
	}) - 1

	p := tl.passes[q]
	local := t - p.start

	// Walk the pass in display order and locate the half-open interval
	// that contains local. Positive-duration intervals partition
	// [0, passLen), so the search always succeeds.
	var cum time.Duration

	for k := 0; k < tl.n; k++ {
		idx := k
		if p.desc {
			idx = tl.n - 1 - k
		}

		d := tl.frames[idx].Duration

		if d != 0 && local >= cum && local < cum+d {
			return Sample{
				Time:  p.start + cum,
				Frame: tl.tag.Lo + idx,
				Loop:  p.loop,
			}, nil
		}

		cum += d
	}

	// Unreachable given the Total and half-open interval guarantees.
	return Sample{}, errTimelineEnded
}

// Total returns the total duration of the timeline.
// It is zero when every frame in the tag range has a zero duration.
func (tl *Timeline) Total() time.Duration {
	return tl.total
}

// Tag returns a copy of the tag the timeline was built from.
func (tl *Timeline) Tag() Tag {
	return tl.tag
}

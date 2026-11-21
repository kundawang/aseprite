package aseprite

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func makeFrames(durations ...time.Duration) []Frame {
	frames := make([]Frame, len(durations))
	for i, d := range durations {
		frames[i].Duration = d
	}

	return frames
}

// drain consumes the whole timeline and returns every sample.
func drain(t *testing.T, tl *Timeline) []Sample {
	t.Helper()

	var samples []Sample

	for {
		s, err := tl.Next()
		if err == errTimelineEnded {
			break
		} else if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		samples = append(samples, s)
	}

	// Next keeps reporting the ended state.
	if _, err := tl.Next(); err != errTimelineEnded {
		t.Fatalf("Next after end = %v, want errTimelineEnded", err)
	}

	return samples
}

// wantSample is a hand-calculated point on the timeline.
type wantSample struct {
	time  time.Duration
	frame int
	loop  int
}

func TestTimelineHandCalculated(t *testing.T) {
	// Five frames with distinct durations so that boundaries are
	// non-obvious and every edge can be checked by hand.
	frames := makeFrames(10, 20, 0, 30, 40)

	tests := []struct {
		name    string
		tag     Tag
		samples []wantSample
		total   time.Duration
	}{
		{
			name: "forward",
			tag:  Tag{Lo: 0, Hi: 4, Repeat: 2, Flags: int(Forward)},
			samples: []wantSample{
				{0, 0, 0}, {10, 1, 0}, {30, 3, 0}, {60, 4, 0},
				{100, 0, 1}, {110, 1, 1}, {130, 3, 1}, {160, 4, 1},
			},
			total: 200,
		},
		{
			name: "reverse",
			tag:  Tag{Lo: 0, Hi: 4, Repeat: 2, Flags: int(Reverse)},
			samples: []wantSample{
				{0, 4, 0}, {40, 3, 0}, {70, 1, 0}, {90, 0, 0},
				{100, 4, 1}, {140, 3, 1}, {170, 1, 1}, {190, 0, 1},
			},
			total: 200,
		},
		{
			name: "pingpong",
			tag:  Tag{Lo: 0, Hi: 4, Repeat: 2, Flags: int(PingPong)},
			samples: []wantSample{
				// forward pass of loop 0; zero-duration frame 2 skipped
				{0, 0, 0}, {10, 1, 0}, {30, 3, 0}, {60, 4, 0},
				// reverse pass of loop 0; boundary 100 belongs to this pass
				{100, 4, 0}, {140, 3, 0}, {170, 1, 0}, {190, 0, 0},
				// loop 1
				{200, 0, 1}, {210, 1, 1}, {230, 3, 1}, {260, 4, 1},
				{300, 4, 1}, {340, 3, 1}, {370, 1, 1}, {390, 0, 1},
			},
			total: 400,
		},
		{
			name: "pingpongreverse",
			tag:  Tag{Lo: 0, Hi: 4, Repeat: 2, Flags: int(PingPongReverse)},
			samples: []wantSample{
				// reverse pass first
				{0, 4, 0}, {40, 3, 0}, {70, 1, 0}, {90, 0, 0},
				{100, 0, 0}, {110, 1, 0}, {130, 3, 0}, {160, 4, 0},
				{200, 4, 1}, {240, 3, 1}, {270, 1, 1}, {290, 0, 1},
				{300, 0, 1}, {310, 1, 1}, {330, 3, 1}, {360, 4, 1},
			},
			total: 400,
		},
		{
			name: "subrange",
			tag:  Tag{Lo: 1, Hi: 3, Repeat: 1, Flags: int(Forward)},
			samples: []wantSample{
				{0, 1, 0}, {20, 3, 0},
			},
			total: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tag := tt.tag
			tl, err := NewTimeline(&tag, frames)
			if err != nil {
				t.Fatal(err)
			}

			if got := tl.Total(); got != tt.total {
				t.Fatalf("Total = %v, want %v", got, tt.total)
			}

			got := drain(t, tl)
			if len(got) != len(tt.samples) {
				t.Fatalf("got %d samples, want %d: %v", len(got), len(tt.samples), got)
			}

			for i, want := range tt.samples {
				if got[i].Time != want.time || got[i].Frame != want.frame || got[i].Loop != want.loop {
					t.Fatalf("sample %d = %+v, want {time:%v frame:%d loop:%d}",
						i, got[i], want.time, want.frame, want.loop)
				}
			}

			// Sample times must be strictly increasing.
			for i := 1; i < len(got); i++ {
				if got[i].Time <= got[i-1].Time {
					t.Fatalf("sample times not strictly increasing at %d: %v", i, got)
				}
			}
		})
	}
}

func TestTimelineAtHandCalculated(t *testing.T) {
	frames := makeFrames(10, 20, 30)
	tag := Tag{Lo: 0, Hi: 2, Repeat: 2, Flags: int(Forward)}
	tl, err := NewTimeline(&tag, frames)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		t     time.Duration
		frame int
		start time.Duration
		loop  int
	}{
		// interior points resolve to the containing half-open interval
		{0, 0, 0, 0},
		{5, 0, 0, 0},
		{9, 0, 0, 0},
		// boundary 10 belongs to frame 1, not frame 0
		{10, 1, 10, 0},
		{29, 1, 10, 0},
		{30, 2, 30, 0},
		{59, 2, 30, 0},
		// boundary 60 belongs to loop 1, frame 0
		{60, 0, 60, 1},
		{61, 0, 60, 1},
		{119, 2, 90, 1},
	}

	for _, tt := range tests {
		s, err := tl.At(tt.t)
		if err != nil {
			t.Fatalf("At(%v): %v", tt.t, err)
		}

		if s.Frame != tt.frame || s.Time != tt.start || s.Loop != tt.loop {
			t.Fatalf("At(%v) = %+v, want frame:%d start:%v loop:%d",
				tt.t, s, tt.frame, tt.start, tt.loop)
		}
	}

	// Half-open end: Total is not on the timeline.
	if _, err := tl.At(tl.Total()); err != errTimelineEnded {
		t.Fatalf("At(Total) err = %v, want errTimelineEnded", err)
	}

	if _, err := tl.At(math.MaxInt64); err != errTimelineEnded {
		t.Fatalf("At(MaxInt64) err = %v, want errTimelineEnded", err)
	}

	if _, err := tl.At(-1); err != errInvalidTime {
		t.Fatalf("At(-1) err = %v, want errInvalidTime", err)
	}
}

func TestTimelineAtZeroDuration(t *testing.T) {
	// Frames 0 and 2 have zero duration; only frame 1 occupies time.
	frames := makeFrames(0, 25, 0)
	tag := Tag{Lo: 0, Hi: 2, Repeat: 2, Flags: int(Forward)}
	tl, err := NewTimeline(&tag, frames)
	if err != nil {
		t.Fatal(err)
	}

	if tl.Total() != 50 {
		t.Fatalf("Total = %v, want 50", tl.Total())
	}

	for _, tq := range []time.Duration{0, 1, 24, 25, 26, 49} {
		s, err := tl.At(tq)
		if err != nil {
			t.Fatalf("At(%v): %v", tq, err)
		}

		if s.Frame != 1 {
			t.Fatalf("At(%v) frame = %d, want 1", tq, s.Frame)
		}
	}

	// The zero-duration frames are skipped by Next as well.
	got := drain(t, tl)
	wantFrames := []int{1, 1}
	if len(got) != len(wantFrames) {
		t.Fatalf("drain = %v, want %d samples", got, len(wantFrames))
	}

	for i, f := range wantFrames {
		if got[i].Frame != f || got[i].Loop != i {
			t.Fatalf("sample %d = %+v, want frame %d loop %d", i, got[i], f, i)
		}
	}
}

// TestTimelineRandom cross-checks the iterator against an independently
// simulated timeline using random frame durations, including zero durations.
func TestTimelineRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	directions := []LoopDirection{Forward, Reverse, PingPong, PingPongReverse}

	for iter := 0; iter < 200; iter++ {
		n := 1 + rng.Intn(8)
		frames := make([]Frame, n)

		var passLen time.Duration

		for i := range frames {
			// Bias toward zero durations so zero-length frames occur often.
			var d time.Duration
			if rng.Intn(3) == 0 {
				d = 0
			} else {
				d = time.Duration(1+rng.Intn(100)) * time.Millisecond
			}

			frames[i].Duration = d
			passLen += d
		}

		repeat := 1 + rng.Intn(5)
		dir := directions[rng.Intn(len(directions))]
		tag := Tag{Lo: 0, Hi: n - 1, Repeat: repeat, Flags: int(dir)}

		tl, err := NewTimeline(&tag, frames)
		if err != nil {
			t.Fatalf("iter %d: NewTimeline: %v", iter, err)
		}

		npasses := repeat
		if dir == PingPong || dir == PingPongReverse {
			npasses = 2 * repeat
		}

		if want := passLen * time.Duration(npasses); tl.Total() != want {
			t.Fatalf("iter %d: Total = %v, want %v", iter, tl.Total(), want)
		}

		// Independent simulation of the expected samples.
		var want []wantSample

		for q := 0; q < npasses; q++ {
			var loop int

			desc := false

			switch dir {
			case Forward:
				loop = q
			case Reverse:
				loop = q
				desc = true
			case PingPong:
				loop = q / 2
				desc = q%2 == 1
			case PingPongReverse:
				loop = q / 2
				desc = q%2 == 0
			}

			for k := 0; k < n; k++ {
				idx := k
				if desc {
					idx = n - 1 - k
				}

				if frames[idx].Duration == 0 {
					continue
				}

				want = append(want, wantSample{frame: idx, loop: loop})
			}
		}

		got := drain(t, tl)

		if len(got) != len(want) {
			t.Fatalf("iter %d: got %d samples, want %d", iter, len(got), len(want))
		}

		var clock time.Duration

		for i := range want {
			if got[i].Frame != want[i].frame || got[i].Loop != want[i].loop {
				t.Fatalf("iter %d sample %d: got %+v, want frame %d loop %d",
					iter, i, got[i], want[i].frame, want[i].loop)
			}

			if got[i].Time != clock {
				t.Fatalf("iter %d sample %d: time = %v, want %v",
					iter, i, got[i].Time, clock)
			}

			if i > 0 && got[i].Time <= got[i-1].Time {
				t.Fatalf("iter %d: non-increasing sample time at %d", iter, i)
			}

			clock += frames[got[i].Frame].Duration
		}

		if clock != tl.Total() {
			t.Fatalf("iter %d: simulated duration %v != Total %v", iter, clock, tl.Total())
		}

		// At must agree with the iterator at every sample start and at
		// points just before each boundary.
		for _, s := range got {
			qs, err := tl.At(s.Time)
			if err != nil {
				t.Fatalf("iter %d: At(%v): %v", iter, s.Time, err)
			}

			if qs != s {
				t.Fatalf("iter %d: At(%v) = %+v, want %+v", iter, s.Time, qs, s)
			}

			if d := frames[s.Frame].Duration; d > 1 {
				qs, err = tl.At(s.Time + d - 1)
				if err != nil || qs.Frame != s.Frame {
					t.Fatalf("iter %d: At(%v) = %+v, %v; want frame %d",
						iter, s.Time+d-1, qs, err, s.Frame)
				}
			}
		}
	}
}

func TestTimelineSingleFrame(t *testing.T) {
	// A one-frame tag has nowhere to turn around. PingPong emits the
	// frame once per pass with no duplicated boundary sample.
	frames := makeFrames(15)
	names := map[LoopDirection]string{
		Forward:         "forward",
		Reverse:         "reverse",
		PingPong:        "pingpong",
		PingPongReverse: "pingpongreverse",
	}

	for _, dir := range []LoopDirection{Forward, Reverse, PingPong, PingPongReverse} {
		dir := dir
		t.Run(names[dir], func(t *testing.T) {
			tag := Tag{Lo: 0, Hi: 0, Repeat: 3, Flags: int(dir)}

			tl, err := NewTimeline(&tag, frames)
			if err != nil {
				t.Fatal(err)
			}

			npasses := 3
			if dir == PingPong || dir == PingPongReverse {
				npasses = 6
			}

			if tl.Total() != 15*time.Duration(npasses) {
				t.Fatalf("Total = %v, want %v", tl.Total(), 15*time.Duration(npasses))
			}

			got := drain(t, tl)
			if len(got) != npasses {
				t.Fatalf("got %d samples, want %d", len(got), npasses)
			}

			for i, s := range got {
				wantLoop := i
				if dir == PingPong || dir == PingPongReverse {
					// Two passes share each loop number.
					wantLoop = i / 2
				}

				if s.Frame != 0 || s.Loop != wantLoop || s.Time != 15*time.Duration(i) {
					t.Fatalf("sample %d = %+v, want loop %d", i, s, wantLoop)
				}
			}
		})
	}
}

func TestTimelineErrorsAndImmutability(t *testing.T) {
	frames := makeFrames(10, 20, 30)

	t.Run("nil tag", func(t *testing.T) {
		if _, err := NewTimeline(nil, frames); err != errNilTag {
			t.Fatalf("err = %v, want errNilTag", err)
		}
	})

	t.Run("empty tag", func(t *testing.T) {
		tag := Tag{Lo: 2, Hi: 1, Repeat: 1, Flags: int(Forward)}
		if _, err := NewTimeline(&tag, frames); err != errEmptyTag {
			t.Fatalf("err = %v, want errEmptyTag", err)
		}
	})

	t.Run("lo negative", func(t *testing.T) {
		tag := Tag{Lo: -1, Hi: 2, Repeat: 1, Flags: int(Forward)}
		if _, err := NewTimeline(&tag, frames); err != errTagOutOfBounds {
			t.Fatalf("err = %v, want errTagOutOfBounds", err)
		}
	})

	t.Run("hi too large", func(t *testing.T) {
		tag := Tag{Lo: 0, Hi: 3, Repeat: 1, Flags: int(Forward)}
		if _, err := NewTimeline(&tag, frames); err != errTagOutOfBounds {
			t.Fatalf("err = %v, want errTagOutOfBounds", err)
		}
	})

	t.Run("zero repeat", func(t *testing.T) {
		tag := Tag{Lo: 0, Hi: 2, Repeat: 0, Flags: int(Forward)}
		before := tag
		_, err := NewTimeline(&tag, frames)

		if err != errZeroRepeat {
			t.Fatalf("err = %v, want errZeroRepeat", err)
		}

		if tag != before {
			t.Fatalf("tag modified: got %+v, want %+v", tag, before)
		}
	})

	t.Run("negative duration", func(t *testing.T) {
		bad := makeFrames(10, -5, 30)
		tag := Tag{Lo: 0, Hi: 2, Repeat: 1, Flags: int(Forward)}
		if _, err := NewTimeline(&tag, bad); err != errNegativeDuration {
			t.Fatalf("err = %v, want errNegativeDuration", err)
		}
	})

	t.Run("tag is not modified", func(t *testing.T) {
		tag := Tag{Name: "x", Flags: 7, Repeat: 4, Lo: 1, Hi: 2}
		before := tag

		tl, err := NewTimeline(&tag, frames)
		if err != nil {
			t.Fatal(err)
		}

		if tag != before {
			t.Fatalf("input tag modified: got %+v, want %+v", tag, before)
		}

		if tl.Tag() != before {
			t.Fatalf("Tag() = %+v, want %+v", tl.Tag(), before)
		}

		// Mutating the caller's tag after construction must not reach
		// the timeline's copy.
		tag.Repeat = 999
		if tl.Tag().Repeat != 4 {
			t.Fatalf("timeline tag aliases caller memory")
		}
	})
}

func TestTimelineOverflow(t *testing.T) {
	max := time.Duration(math.MaxInt64)

	t.Run("pass duration overflow", func(t *testing.T) {
		frames := makeFrames(max, max)
		tag := Tag{Lo: 0, Hi: 1, Repeat: 1, Flags: int(Forward)}

		if _, err := NewTimeline(&tag, frames); err != errDurationOverflow {
			t.Fatalf("err = %v, want errDurationOverflow", err)
		}
	})

	t.Run("repeat multiplication overflow", func(t *testing.T) {
		frames := makeFrames(max / 2)
		tag := Tag{Lo: 0, Hi: 0, Repeat: 3, Flags: int(Forward)}

		if _, err := NewTimeline(&tag, frames); err != errDurationOverflow {
			t.Fatalf("err = %v, want errDurationOverflow", err)
		}
	})

	t.Run("pingpong doubles overflow", func(t *testing.T) {
		// Two passes per loop double a pass length that already
		// overflows when doubled.
		frames := makeFrames(time.Duration(math.MaxInt64/2 + 1))
		tag := Tag{Lo: 0, Hi: 0, Repeat: 1, Flags: int(PingPong)}

		if _, err := NewTimeline(&tag, frames); err != errDurationOverflow {
			t.Fatalf("err = %v, want errDurationOverflow", err)
		}
	})

	t.Run("largest finite total is accepted", func(t *testing.T) {
		frames := makeFrames(max / 3)
		tag := Tag{Lo: 0, Hi: 0, Repeat: 3, Flags: int(Forward)}

		tl, err := NewTimeline(&tag, frames)
		if err != nil {
			t.Fatal(err)
		}

		if tl.Total() != max/3*3 {
			t.Fatalf("Total = %v, want %v", tl.Total(), max/3*3)
		}

		if _, err := tl.At(max); err != errTimelineEnded {
			t.Fatalf("At(MaxInt64) err = %v, want errTimelineEnded", err)
		}
	})
}

func TestTimelineAllZeroDurations(t *testing.T) {
	frames := makeFrames(0, 0, 0)
	tag := Tag{Lo: 0, Hi: 2, Repeat: 2, Flags: int(PingPong)}

	tl, err := NewTimeline(&tag, frames)
	if err != nil {
		t.Fatal(err)
	}

	if tl.Total() != 0 {
		t.Fatalf("Total = %v, want 0", tl.Total())
	}

	if got := drain(t, tl); len(got) != 0 {
		t.Fatalf("drain = %v, want no samples", got)
	}

	if _, err := tl.At(0); err != errTimelineEnded {
		t.Fatalf("At(0) err = %v, want errTimelineEnded", err)
	}
}

// FuzzTimeline re-derives every sample with an independent clock model and
// checks that At agrees with Next at every boundary.
func FuzzTimeline(f *testing.F) {
	f.Add(uint64(1), 2, 1, uint8(Forward))
	f.Add(uint64(123456789), 8, 4, uint8(PingPong))
	f.Add(uint64(987654321), 1, 5, uint8(PingPongReverse))

	f.Fuzz(func(t *testing.T, seed uint64, nframes, repeat int, dirbyte uint8) {
		if nframes < 1 || nframes > 16 || repeat < 1 || repeat > 8 {
			return
		}

		dir := LoopDirection(dirbyte % 4)

		rng := rand.New(rand.NewSource(int64(seed)))
		frames := make([]Frame, nframes)

		var passLen time.Duration

		for i := range frames {
			var d time.Duration

			switch rng.Intn(4) {
			case 0:
				d = 0
			case 1:
				d = 1
			default:
				d = time.Duration(1+rng.Intn(50)) * time.Millisecond
			}

			frames[i].Duration = d
			passLen += d
		}

		tag := Tag{Lo: 0, Hi: nframes - 1, Repeat: repeat, Flags: int(dir)}

		tl, err := NewTimeline(&tag, frames)
		if err != nil {
			t.Fatal(err)
		}

		npasses := repeat
		if dir == PingPong || dir == PingPongReverse {
			npasses = 2 * repeat
		}

		if want := passLen * time.Duration(npasses); tl.Total() != want {
			t.Fatalf("Total = %v, want %v", tl.Total(), want)
		}

		// Independent clock: the running time before each emitted sample
		// must equal the sum of durations of all previously emitted frames.
		var clock time.Duration
		prevLoop := -1
		counts := map[int]int{}

		for {
			s, err := tl.Next()
			if err == errTimelineEnded {
				break
			} else if err != nil {
				t.Fatal(err)
			}

			if s.Time != clock {
				t.Fatalf("Time = %v, clock = %v", s.Time, clock)
			}

			if s.Loop < prevLoop {
				t.Fatalf("loop count went backwards: %d -> %d", prevLoop, s.Loop)
			}

			prevLoop = s.Loop
			counts[s.Loop]++
			clock += frames[s.Frame].Duration

			// At must reproduce the exact sample at its start time.
			qs, err := tl.At(s.Time)
			if err != nil || qs != s {
				t.Fatalf("At(%v) = %+v, %v; want %+v", s.Time, qs, err, s)
			}
		}

		if clock != tl.Total() {
			t.Fatalf("final clock %v != Total %v", clock, tl.Total())
		}

		// Every loop index in [0, repeat) must occur when the timeline
		// has any positive-duration frames.
		if passLen > 0 && len(counts) != repeat {
			t.Fatalf("loops seen = %d, want %d (%v)", len(counts), repeat, counts)
		}
	})
}

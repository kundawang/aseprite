package aseprite

import (
	"image/color"
	"testing"
)

func customFunc() BlendFunc {
	return func(dst, src color.Color) color.Color {
		return color.RGBA{R: 17, G: 209, B: 31, A: 255}
	}
}

func TestDefaultRegistry(t *testing.T) {
	r := DefaultRegistry()
	if r != DefaultRegistry() {
		t.Fatal("DefaultRegistry must return the same instance")
	}

	all := r.All()
	if len(all) != 19 {
		t.Fatalf("expected 19 built-in modes, got %d", len(all))
	}
	for i, info := range all {
		if int(info.Mode) != i {
			t.Fatalf("entry %d has mode %d, want %d", i, info.Mode, i)
		}
		if info.Name == "" {
			t.Fatalf("mode %d has empty name", info.Mode)
		}
		if info.Func == nil {
			t.Fatalf("mode %d has nil func", info.Mode)
		}
	}

	// Name/value/function round trip.
	for _, info := range all {
		if m, ok := r.Mode(info.Name); !ok || m != info.Mode {
			t.Fatalf("Mode(%q) = %d, %v, want %d", info.Name, m, ok, info.Mode)
		}
		if name, ok := r.Name(info.Mode); !ok || name != info.Name {
			t.Fatalf("Name(%d) = %q, %v, want %q", info.Mode, name, ok, info.Name)
		}
		if fn, ok := r.Func(info.Mode); !ok || fn == nil {
			t.Fatalf("Func(%d) missing", info.Mode)
		}
	}
}

func TestModeString(t *testing.T) {
	for _, tt := range []struct {
		mode Mode
		want string
	}{
		{Normal, "Normal"},
		{ColorDodge, "Color Dodge"},
		{HardLight, "Hard Light"},
		{Divide, "Divide"},
		{Mode(19), "Mode(19)"},
		{Mode(65535), "Mode(65535)"},
	} {
		if got := tt.mode.String(); got != tt.want {
			t.Fatalf("%d.String() = %q, want %q", uint16(tt.mode), got, tt.want)
		}
	}
}

func TestNewRegistryValidation(t *testing.T) {
	for _, tt := range []struct {
		name  string
		modes []ModeInfo
	}{
		{"nil func", []ModeInfo{{Mode: 42, Name: "X", Func: nil}}},
		{"empty name", []ModeInfo{{Mode: 42, Name: "", Func: customFunc()}}},
		{"duplicate value", []ModeInfo{
			{Mode: 42, Name: "X", Func: customFunc()},
			{Mode: 42, Name: "Y", Func: customFunc()},
		}},
		{"duplicate name", []ModeInfo{
			{Mode: 42, Name: "X", Func: customFunc()},
			{Mode: 43, Name: "X", Func: customFunc()},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRegistry(tt.modes...); err == nil {
				t.Fatal("expected error")
			}
		})
	}

	if _, err := NewRegistry(); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryImmutability(t *testing.T) {
	base := DefaultRegistry()

	ext, err := base.With(ModeInfo{Mode: 19, Name: "Pipette", Func: customFunc()})
	if err != nil {
		t.Fatal(err)
	}

	// The receiver is untouched.
	if _, ok := base.Lookup(Mode(19)); ok {
		t.Fatal("With mutated the receiver registry")
	}
	if info, ok := ext.Lookup(Mode(19)); !ok || info.Name != "Pipette" {
		t.Fatalf("extended registry missing new entry: %+v %v", info, ok)
	}

	// All returns an independent copy sorted by mode.
	all := ext.All()
	for i := 1; i < len(all); i++ {
		if all[i].Mode < all[i-1].Mode {
			t.Fatal("All() is not sorted by Mode")
		}
	}
	all[0] = ModeInfo{Mode: 250, Name: "Tampered", Func: customFunc()}
	if info, ok := ext.Lookup(Mode(0)); !ok || info.Name != "Normal" {
		t.Fatalf("mutating All() result modified the registry: %+v %v", info, ok)
	}

	// With rejects collisions with existing entries.
	if _, err := ext.With(ModeInfo{Mode: Normal, Name: "Dup", Func: customFunc()}); err == nil {
		t.Fatal("expected duplicate value error")
	}
	if _, err := ext.With(ModeInfo{Mode: 20, Name: "Normal", Func: customFunc()}); err == nil {
		t.Fatal("expected duplicate name error")
	}
}

func TestKindFor(t *testing.T) {
	r := DefaultRegistry()
	for _, tt := range []struct {
		mode Mode
		want DiagnosticKind
	}{
		{Normal, 0},
		{Divide, 0},
		{Mode(19), KindReserved},
		{Mode(255), KindReserved},
		{Mode(256), KindOutOfRange},
		{Mode(65535), KindOutOfRange},
	} {
		if got := kindFor(r, tt.mode); got != tt.want {
			t.Fatalf("kindFor(%d) = %v, want %v", uint16(tt.mode), got, tt.want)
		}
	}

	// A built-in value missing from a custom registry is unknown, not
	// reserved, because it lies in the defined mode range.
	custom, err := NewRegistry(ModeInfo{Normal, "Normal", customFunc()})
	if err != nil {
		t.Fatal(err)
	}
	if got := kindFor(custom, Lighten); got != KindUnknown {
		t.Fatalf("kindFor(Lighten) = %v, want KindUnknown", got)
	}
	if got := kindFor(custom, Mode(19)); got != KindReserved {
		t.Fatalf("kindFor(19) = %v, want KindReserved", got)
	}
	if got := kindFor(custom, Mode(256)); got != KindOutOfRange {
		t.Fatalf("kindFor(256) = %v, want KindOutOfRange", got)
	}
}

func TestDiagnosticKindString(t *testing.T) {
	for _, tt := range []struct {
		kind DiagnosticKind
		want string
	}{
		{KindUnknown, "unknown"},
		{KindReserved, "reserved"},
		{KindOutOfRange, "out of range"},
	} {
		if got := tt.kind.String(); got != tt.want {
			t.Fatalf("%v.String() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestUnsupportedModeError(t *testing.T) {
	err := &UnsupportedModeError{Diagnostic{
		Frame: 2, Layer: 1, Mode: Mode(19), Kind: KindReserved,
	}}
	want := "aseprite: unsupported blend mode 19 (reserved) on layer 1 frame 2"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestDecodeWithOptionsFallbackValidation(t *testing.T) {
	r, err := NewRegistry(ModeInfo{Normal, "Normal", customFunc()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeWithOptions(nil, DecodeOptions{Registry: r, Fallback: Multiply}); err == nil {
		t.Fatal("expected error for unregistered fallback")
	}
}

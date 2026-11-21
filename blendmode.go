package aseprite

import (
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/askeladdk/aseprite/internal/blend"
)

// Mode is the blend mode value stored in the blend mode field of a layer
// chunk in an Aseprite file. The values match the Aseprite file format
// specification.
//
// The Aseprite specification defines modes 0 through 18. Values 19 through
// 255 are reserved for future modes and values 256 through 65535 are out of
// range.
type Mode uint16

// Blend mode values as defined by the Aseprite file format specification.
const (
	Normal     Mode = 0
	Multiply   Mode = 1
	Screen     Mode = 2
	Overlay    Mode = 3
	Darken     Mode = 4
	Lighten    Mode = 5
	ColorDodge Mode = 6
	ColorBurn  Mode = 7
	HardLight  Mode = 8
	SoftLight  Mode = 9
	Difference Mode = 10
	Exclusion  Mode = 11
	Hue        Mode = 12
	Saturation Mode = 13
	Color      Mode = 14
	Luminosity Mode = 15
	Addition   Mode = 16
	Subtract   Mode = 17
	Divide     Mode = 18
)

// reservedLimit is the highest value reserved by the Aseprite specification
// for future blend modes. Higher values that still fit the uint16 layer
// field are reported as out of range.
const reservedLimit Mode = 255

// String returns the stable Aseprite name of a built-in mode, for example
// "Color Dodge" for ColorDodge. Values without a built-in name are rendered
// as "Mode(n)".
func (m Mode) String() string {
	if info, ok := builtinNames[m]; ok {
		return info
	}
	return fmt.Sprintf("Mode(%d)", uint16(m))
}

// builtinNames maps the built-in mode values to their stable Aseprite names.
var builtinNames = map[Mode]string{
	Normal:     "Normal",
	Multiply:   "Multiply",
	Screen:     "Screen",
	Overlay:    "Overlay",
	Darken:     "Darken",
	Lighten:    "Lighten",
	ColorDodge: "Color Dodge",
	ColorBurn:  "Color Burn",
	HardLight:  "Hard Light",
	SoftLight:  "Soft Light",
	Difference: "Difference",
	Exclusion:  "Exclusion",
	Hue:        "Hue",
	Saturation: "Saturation",
	Color:      "Color",
	Luminosity: "Luminosity",
	Addition:   "Addition",
	Subtract:   "Subtract",
	Divide:     "Divide",
}

// BlendFunc blends a destination color (the accumulated bottom layers) with
// a source color (the top layer cel) and returns the blended color.
//
// Alpha rules:
//
//   - Colors follow the premultiplied-alpha convention of image/color: the
//     components returned by color.RGBA are scaled by alpha and range up to
//     65535.
//   - A BlendFunc is only called for opaque source pixels. Fully transparent
//     source pixels are copied through without blending, matching Aseprite.
//   - The returned color must be fully opaque (alpha 65535). The built-in
//     modes all return opaque colors.
//   - Layer opacity and the cel opacity mask are applied separately by the
//     decoder after blending. A BlendFunc must not incorporate layer or cel
//     opacity into its result.
//
// Implementations must be pure and safe to call concurrently: multiple
// decoders that share a Registry may invoke the same BlendFunc in parallel,
// so it must not rely on or mutate shared state.
type BlendFunc = blend.BlendFunc

// ModeInfo is one entry of a Registry: the numeric value stored in the
// Aseprite file, its stable name and the function that implements it.
type ModeInfo struct {
	// Mode is the numeric value stored in the layer chunk.
	Mode Mode

	// Name is the stable, human-readable mode name. It must be unique within
	// a Registry and must not be empty.
	Name string

	// Func implements the blend mode. It must not be nil.
	Func BlendFunc
}

// builtinModes returns the blend modes defined by the Aseprite file format
// specification.
func builtinModes() []ModeInfo {
	return []ModeInfo{
		{Normal, "Normal", blend.Normal},
		{Multiply, "Multiply", blend.Multiply},
		{Screen, "Screen", blend.Screen},
		{Overlay, "Overlay", blend.Overlay},
		{Darken, "Darken", blend.Darken},
		{Lighten, "Lighten", blend.Lighten},
		{ColorDodge, "Color Dodge", blend.ColorDodge},
		{ColorBurn, "Color Burn", blend.ColorBurn},
		{HardLight, "Hard Light", blend.HardLight},
		{SoftLight, "Soft Light", blend.SoftLight},
		{Difference, "Difference", blend.Difference},
		{Exclusion, "Exclusion", blend.Exclusion},
		{Hue, "Hue", blend.Hue},
		{Saturation, "Saturation", blend.Saturation},
		{Color, "Color", blend.Color},
		{Luminosity, "Luminosity", blend.Luminosity},
		{Addition, "Addition", blend.Addition},
		{Subtract, "Subtract", blend.Subtract},
		{Divide, "Divide", blend.Divide},
	}
}

// Registry is an immutable mapping between blend mode names, the numeric
// values stored in Aseprite files and their blending functions.
//
// Create registries with NewRegistry and extend them with With. The maps are
// unexported and every accessor returns a value or a copy, so callers cannot
// modify a registry after creation. A Registry holds no mutable state and is
// safe for concurrent use by any number of decoders. The zero value is an
// empty, usable registry.
type Registry struct {
	byMode map[Mode]ModeInfo
	byName map[string]ModeInfo
}

// NewRegistry creates a Registry containing exactly the given mode entries.
// The default registry of the decoder is built from the built-in modes, but
// NewRegistry also allows registries with a custom set of modes.
//
// It returns an error when an entry has a nil Func, an empty Name, or when
// two entries share a Mode value or a Name.
func NewRegistry(modes ...ModeInfo) (*Registry, error) {
	r := &Registry{
		byMode: make(map[Mode]ModeInfo, len(modes)),
		byName: make(map[string]ModeInfo, len(modes)),
	}

	for _, m := range modes {
		if m.Func == nil {
			return nil, fmt.Errorf("aseprite: nil blend function for mode %d", uint16(m.Mode))
		}
		if m.Name == "" {
			return nil, fmt.Errorf("aseprite: empty blend mode name for mode %d", uint16(m.Mode))
		}
		if _, exists := r.byMode[m.Mode]; exists {
			return nil, fmt.Errorf("aseprite: duplicate blend mode value %d", uint16(m.Mode))
		}
		if _, exists := r.byName[m.Name]; exists {
			return nil, fmt.Errorf("aseprite: duplicate blend mode name %q", m.Name)
		}
		r.byMode[m.Mode] = m
		r.byName[m.Name] = m
	}

	return r, nil
}

// With returns a new Registry that contains all entries of r plus the given
// entries. The receiver is not modified. It returns an error when an entry
// has a nil Func, an empty Name, or reuses a Mode value or Name already
// present in the returned registry.
func (r *Registry) With(modes ...ModeInfo) (*Registry, error) {
	merged := make([]ModeInfo, 0, len(r.byMode)+len(modes))
	merged = append(merged, r.All()...)
	merged = append(merged, modes...)
	return NewRegistry(merged...)
}

// Lookup returns the entry registered for m and whether it is registered.
func (r *Registry) Lookup(m Mode) (ModeInfo, bool) {
	if r == nil {
		return ModeInfo{}, false
	}
	info, ok := r.byMode[m]
	return info, ok
}

// Name returns the stable name registered for m.
func (r *Registry) Name(m Mode) (string, bool) {
	info, ok := r.Lookup(m)
	return info.Name, ok
}

// Func returns the blending function registered for m.
func (r *Registry) Func(m Mode) (BlendFunc, bool) {
	info, ok := r.Lookup(m)
	return info.Func, ok
}

// Mode returns the mode value registered under name.
func (r *Registry) Mode(name string) (Mode, bool) {
	if r == nil {
		return 0, false
	}
	info, ok := r.byName[name]
	return info.Mode, ok
}

// All returns a copy of all registered mode entries, sorted by Mode value.
// Modifying the returned slice does not affect the registry.
func (r *Registry) All() []ModeInfo {
	if r == nil {
		return nil
	}
	all := make([]ModeInfo, 0, len(r.byMode))
	for _, info := range r.byMode {
		all = append(all, info)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Mode < all[j].Mode })
	return all
}

var (
	defaultRegistryOnce sync.Once
	defaultRegistry     *Registry
)

// DefaultRegistry returns the registry of the blend modes defined by the
// Aseprite file format specification (modes 0 through 18). The same registry
// is returned on every call and is safe to share between decoders.
func DefaultRegistry() *Registry {
	defaultRegistryOnce.Do(func() {
		var err error
		defaultRegistry, err = NewRegistry(builtinModes()...)
		if err != nil {
			panic(err)
		}
	})
	return defaultRegistry
}

// DiagnosticKind classifies an unsupported blend mode.
type DiagnosticKind int

const (
	// KindUnknown means the value lies in the defined mode range (0 through
	// 18) or is otherwise not registered by the active Registry, for example
	// a built-in mode missing from a custom Registry.
	KindUnknown DiagnosticKind = iota + 1

	// KindReserved means the value is reserved by the Aseprite specification
	// for a future blend mode (values 19 through 255).
	KindReserved

	// KindOutOfRange means the value exceeds the reserved range (values 256
	// through 65535).
	KindOutOfRange
)

// String returns "unknown", "reserved" or "out of range".
func (k DiagnosticKind) String() string {
	switch k {
	case KindUnknown:
		return "unknown"
	case KindReserved:
		return "reserved"
	case KindOutOfRange:
		return "out of range"
	default:
		return "unsupported"
	}
}

// kindFor classifies m against the registry. It returns 0 when m is
// registered.
func kindFor(r *Registry, m Mode) DiagnosticKind {
	if _, ok := r.Lookup(m); ok {
		return 0
	}
	if m > Divide && m <= reservedLimit {
		return KindReserved
	}
	if m > reservedLimit {
		return KindOutOfRange
	}
	return KindUnknown
}

// Diagnostic records an unsupported blend mode encountered while flattening
// the cels of a frame. Frame and Layer are zero-based indices matching the
// frame order and layer order of the Aseprite file.
type Diagnostic struct {
	// Frame is the zero-based frame index containing the cel.
	Frame int

	// Layer is the zero-based layer index whose blend mode is unsupported.
	Layer int

	// Mode is the value stored in the file.
	Mode Mode

	// Kind classifies the unsupported mode.
	Kind DiagnosticKind
}

// UnsupportedModeError is returned when DecodeOptions.Strict is enabled and
// a layer references a blend mode that the active Registry does not
// implement.
type UnsupportedModeError struct {
	Diagnostic
}

// Error implements the error interface.
func (e *UnsupportedModeError) Error() string {
	return fmt.Sprintf(
		"aseprite: unsupported blend mode %d (%s) on layer %d frame %d",
		uint16(e.Mode), e.Kind, e.Layer, e.Frame,
	)
}

// DecodeOptions configures blend mode handling for DecodeWithOptions.
type DecodeOptions struct {
	// Registry maps blend mode values to names and blending functions. When
	// nil, DefaultRegistry is used.
	Registry *Registry

	// Strict makes decoding fail with an UnsupportedModeError when a layer
	// references a blend mode that Registry does not implement. When false,
	// the unsupported mode is replaced by Fallback and a Diagnostic is
	// recorded on the decoded Aseprite.
	Strict bool

	// Fallback is the mode substituted for unsupported modes when Strict is
	// false. It must be registered in Registry. The zero value Normal is the
	// default and preserves the historical decoder behavior.
	Fallback Mode
}

// DecodeWithOptions decodes an Aseprite sprite using the given options.
//
// When opts.Strict is true, the first unsupported blend mode aborts decoding
// and returns an *UnsupportedModeError carrying the frame and layer indices.
// Otherwise every unsupported mode is recorded in Aseprite.Diagnostics and
// the cel is composited with opts.Fallback instead.
func DecodeWithOptions(r io.Reader, opts DecodeOptions) (*Aseprite, error) {
	if opts.Registry == nil {
		opts.Registry = DefaultRegistry()
	}
	if _, ok := opts.Registry.Lookup(opts.Fallback); !ok {
		return nil, fmt.Errorf(
			"aseprite: fallback blend mode %d is not registered",
			uint16(opts.Fallback),
		)
	}

	var spr Aseprite
	if err := spr.readFrom(r, opts); err != nil {
		return nil, err
	}
	return &spr, nil
}

package aseprite

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This file implements the fault injection required by the quality plan. It
// recompiles a copy of the package with a chosen, deliberately reintroduced
// boundary defect and asserts that the new corruption tests fail against it.
//
// The mutations are expressed as source substitutions, so the proof is
// mechanical: the unmodified tree passes, the mutated tree does not. The test
// is skipped outside a full module checkout (for example under `go test`
// with an unusual -run filter or when the source is unavailable), because it
// needs to invoke the toolchain.

type mutation struct {
	// Name identifies the defect being reintroduced.
	Name string
	// File and Old/New describe a source rewrite that removes one bounds
	// check. Old must appear exactly once in the file.
	File string
	Old  string
	New  string
	// WantTest is the test that must fail under the mutation.
	WantTest string
}

func mutations() []mutation {
	return []mutation{
		{
			Name:     "chunk_length_ignores_frame_bound",
			File:     "file.go",
			Old:      "\tif chunkLen > limit {\n\t\treturn nil, ctx.err(ErrMalformed, \"chunk type 0x%04x length %d exceeds the %d bytes left in the frame\", typ, chunkLen, limit)\n\t}\n",
			New:      "",
			WantTest: "TestCorruptMatrix/chunk/length_overruns_frame",
		},
		{
			Name:     "link_source_index_unchecked",
			File:     "parse.go",
			Old:      "\t\tif int(src) >= len(f.frames) {\n\t\t\treturn nil, ctx.err(ErrMalformed,\n\t\t\t\t\"linked cel references frame %d, but the file has %d frames\", src, len(f.frames))\n\t\t}\n",
			New:      "",
			WantTest: "TestCorruptMatrix/linked/frame_out_of_range",
		},
		{
			Name:     "zlib_output_length_unchecked",
			File:     "parse.go",
			Old:      "\tif int64(len(pix)) != want {\n\t\treturn nil, ctx.err(ErrMalformed, \"cel of %dx%d needs %d pixel bytes, decoded %d\",\n\t\t\twidth, height, want, len(pix))\n\t}\n",
			New:      "",
			WantTest: "TestCorruptMatrix/zlib/short_output",
		},
		{
			Name:     "palette_range_unchecked",
			File:     "parse.go",
			Old:      "\tif uint64(lo)+uint64(entries) > uint64(len(f.palette)) {\n\t\treturn ctx.err(ErrMalformed, \"palette entries [%d,%d) exceed the %d-colour palette\",\n\t\t\tlo, uint64(lo)+uint64(entries), len(f.palette))\n\t}\n",
			New:      "",
			WantTest: "TestCorruptMatrix/palette/entries_past_palette",
		},
	}
}

// TestFaultInjection proves that at least two existing boundary defects are
// caught by the new tests. Each mutation removes one production guard; the
// harness then runs the named corruption case in a rebuilt copy and requires
// it to fail (or panic).
func TestFaultInjection(t *testing.T) {
	if testing.Short() {
		t.Skip("fault injection rebuilds the package")
	}

	_, srcFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot locate source")
	}
	srcDir := filepath.Dir(srcFile)

	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}

	if err := verifyFullPackage(srcDir); err != nil {
		t.Skipf("not a full package checkout: %v", err)
	}

	work, err := os.MkdirTemp("", "aseprite-fault-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })

	// Copy the module once; each mutation is applied to a fresh copy of the
	// relevant file so the runs cannot contaminate one another.
	if err := copyModule(srcDir, work); err != nil {
		t.Fatal(err)
	}

	var proven int
	for _, m := range mutations() {
		m := m
		t.Run(m.Name, func(t *testing.T) {
			dir := t.TempDir()
			if err := copyModule(work, dir); err != nil {
				t.Fatal(err)
			}
			if err := applyMutation(dir, m); err != nil {
				t.Fatal(err)
			}

			out, err := runTest(dir, m.WantTest)
			if err == nil {
				t.Fatalf("mutation %q did not break %s; the test does not guard this boundary\n%s",
					m.Name, m.WantTest, out)
			}
			t.Logf("mutation %q correctly failed %s", m.Name, m.WantTest)
		})
		proven++
	}

	if proven < 2 {
		t.Fatalf("only %d mutations were exercised, need at least 2", proven)
	}
}

// TestFaultInjectionBaseline asserts the copied, unmutated tree still passes
// the same case, so a failure above is attributable to the mutation and not to
// the copy step.
func TestFaultInjectionBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("fault injection rebuilds the package")
	}
	_, srcFile, _, _ := runtime.Caller(0)
	srcDir := filepath.Dir(srcFile)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}
	if err := verifyFullPackage(srcDir); err != nil {
		t.Skipf("not a full package checkout: %v", err)
	}

	work, err := os.MkdirTemp("", "aseprite-baseline-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })
	if err := copyModule(srcDir, work); err != nil {
		t.Fatal(err)
	}
	if out, err := runTest(work, "TestCorruptMatrix"); err != nil {
		t.Fatalf("unmutated copy failed: %v\n%s", err, out)
	}
}

func applyMutation(dir string, m mutation) error {
	path := filepath.Join(dir, m.File)
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if n := bytes.Count(src, []byte(m.Old)); n != 1 {
		return errors.New("mutation anchor appears " + itoa(n) + " times in " + m.File)
	}
	mutated := bytes.Replace(src, []byte(m.Old), []byte(m.New), 1)
	return os.WriteFile(path, mutated, 0o644)
}

func runTest(dir, run string) ([]byte, error) {
	cmd := exec.Command("go", "test", "-count=1", "-run", "^"+run+"$", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "CGO_ENABLED=0")
	return cmd.CombinedOutput()
}

// verifyFullPackage checks that the directory looks like the module root with
// all source files present, which the rebuild needs.
func verifyFullPackage(dir string) error {
	for _, name := range []string{"go.mod", "file.go", "parse.go", "errors.go", "aseprite.go"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	if _, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, "file.go"), nil, 0); err != nil {
		return err
	}
	return nil
}

func copyModule(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if name == ".git" || name == "artifacts" || strings.HasSuffix(name, ".png") {
			continue
		}
		s := filepath.Join(src, name)
		d := filepath.Join(dst, name)
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := os.MkdirAll(d, info.Mode()); err != nil {
				return err
			}
			if err := copyModule(s, d); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		if err := os.WriteFile(d, data, info.Mode()); err != nil {
			return err
		}
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

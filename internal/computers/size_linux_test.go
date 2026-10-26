//go:build linux

package computers

import (
	"dyna-agent/internal/testsafe"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestComputerSizeSurvivesSupervisorRestart(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	id := testSessionID(t, "saved-size")
	if err := os.MkdirAll(Dir(id), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(Status{SessionID: id, Width: 390, Height: 844})
	if err := os.WriteFile(filepath.Join(Dir(id), "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := newComputer(t.Context(), id, "test", SupervisorOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.cleanup(false); err != nil {
			t.Error(err)
		}
	})
	if s.state.Width != 390 || s.state.Height != 844 {
		t.Fatalf("saved size became %dx%d", s.state.Width, s.state.Height)
	}
	if s.compositor.alive() {
		t.Fatal("restoring a size started a desktop")
	}
}

func TestComputerSizeRestartDoesNotRelabelCachedFrame(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	bin := t.TempDir()
	source := filepath.Join(bin, "frame.png")
	writeFrame := func(width, height int) {
		t.Helper()
		f, err := os.Create(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "grim"), []byte("#!/bin/sh\nexec /bin/cp '"+source+"' \"$3\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	id := testSessionID(t, "resized-cache")
	if err := os.MkdirAll(Dir(id), 0700); err != nil {
		t.Fatal(err)
	}
	first := &supervisor{ctx: t.Context(), meta: Meta{SessionID: id}, state: Status{SessionID: id, Width: 390, Height: 844, Control: ControlAgent}, inputGate: make(chan struct{}, 1), compositor: &process{done: make(chan struct{})}}
	writeFrame(390, 844)
	before, err := first.capture(true, false)
	if err != nil {
		t.Fatal(err)
	}
	// No app is connected while configuration changes. The last PNG stays on disk.
	first.compositor = nil
	if _, err := first.resize(t.Context(), Request{Op: "size", Status: "Checking landscape", Size: &Size{Width: 844, Height: 390}}); err != nil {
		t.Fatal(err)
	}
	restored, err := newComputer(t.Context(), id, "test", SupervisorOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := restored.cleanup(false); err != nil {
			t.Error(err)
		}
	})
	// Exercise cached capture after startup without another GUI. The same fake
	// grim used above now supplies the running desktop's new landscape frame.
	restored.mu.Lock()
	running := &supervisor{ctx: t.Context(), meta: restored.meta, state: restored.state, shot: restored.shot, compositor: &process{done: make(chan struct{})}}
	restored.mu.Unlock()
	writeFrame(844, 390)
	after, err := running.capture(false, false)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(after.Path)
	if err != nil {
		t.Fatal(err)
	}
	dimensions, err := png.DecodeConfig(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if dimensions.Width != 844 || dimensions.Height != 390 || after.Width != 844 || after.Height != 390 {
		t.Fatalf("cached capture metadata %dx%d, PNG %dx%d; want 844x390", after.Width, after.Height, dimensions.Width, dimensions.Height)
	}
	if !after.CapturedAt.After(before.CapturedAt) {
		t.Fatal("restart reused the pre-resize screenshot")
	}
}

// A loaded host can answer a new desktop's first wlr-randr request later than
// a live resize allows. startDesktop sizes the output through sizeNewDesktop,
// which must use the start budget: with a fake wlr-randr that answers after
// 100 ms, a 50 ms start budget fails and a 600 ms one succeeds. A version that
// used liveOutputTimeout (3 s) would pass both.
func TestNewDesktopSizeUsesTheStartBudget(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "wlr-randr"), []byte("#!/bin/sh\nsleep 0.1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	short := &supervisor{startOutput: 50 * time.Millisecond}
	if err := short.sizeNewDesktop(t.Context(), os.Environ(), 1280, 800); err == nil {
		t.Fatal("a 50 ms start budget allowed a 100 ms wlr-randr")
	}
	enough := &supervisor{startOutput: 600 * time.Millisecond}
	if err := enough.sizeNewDesktop(t.Context(), os.Environ(), 1280, 800); err != nil {
		t.Fatalf("start budget: %v", err)
	}
	if (&supervisor{}).startOutputBudget() != defaultStartOutputTimeout {
		t.Fatal("a zero start budget must mean the default")
	}
}

package computers

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestFreshShotAlwaysCapturesAgain(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bin := t.TempDir()
	source := filepath.Join(bin, "frame.png")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "grim"), []byte("#!/bin/sh\nexec /bin/cp '"+source+"' \"$3\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	id := "fresh-shot"
	if err := os.MkdirAll(Dir(id), 0700); err != nil {
		t.Fatal(err)
	}
	s := &supervisor{ctx: context.Background(), meta: Meta{SessionID: id}, state: Status{Width: 2, Height: 2}, compositor: &process{done: make(chan struct{})}}
	first, err := s.capture(true, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.capture(true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !second.CapturedAt.After(first.CapturedAt) {
		t.Fatalf("fresh shot reused %s", first.CapturedAt)
	}
	cached, err := s.capture(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cached.CapturedAt.Equal(second.CapturedAt) {
		t.Fatal("preview did not reuse its cached frame")
	}
}

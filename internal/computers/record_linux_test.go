package computers

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recordFixture(t *testing.T) (*supervisor, string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bin := t.TempDir()
	var frame bytes.Buffer
	if err := png.Encode(&frame, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(bin, "frame.png")
	if err := os.WriteFile(source, frame.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(bin, "ffmpeg.args")
	// The fake grim prints one PNG. The fake ffmpeg logs its arguments and copies the frames it reads to the output file.
	if err := os.WriteFile(filepath.Join(bin, "grim"), []byte("#!/bin/sh\nexec /bin/cat '"+source+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte("#!/bin/sh\necho \"$@\" > '"+args+"'\nfor last; do :; done\nexec /bin/cat > \"$last\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/bin:/usr/bin")
	id := "record-test"
	if err := os.MkdirAll(Dir(id), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &supervisor{ctx: context.Background(), meta: Meta{SessionID: id}, state: Status{Width: 4, Height: 4}, compositor: &process{done: make(chan struct{})}}
	return s, args
}

func TestRecordCapsBitrateWritesAClipAndShowsRecording(t *testing.T) {
	s, argsFile := recordFixture(t)
	for _, bad := range []Request{{RecordSeconds: 61}, {RecordFPS: 11}, {RecordKbps: 4001}, {RecordSeconds: 60, RecordKbps: 4000}, {RecordSeconds: -1}, {RecordFPS: -1}, {RecordKbps: -1}} {
		if _, err := s.startRecord(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if _, err := s.stopRecord(); err == nil || !strings.Contains(err.Error(), "no recording") {
		t.Fatalf("stop before start: %v", err)
	}
	started, err := s.startRecord(Request{RecordSeconds: 5, RecordFPS: 10, RecordKbps: 500})
	if err != nil || !started.Running || !s.state.Recording {
		t.Fatalf("start: %+v %v recording=%v", started, err, s.state.Recording)
	}
	if _, err := s.startRecord(Request{}); err == nil || !strings.Contains(err.Error(), "already recording") {
		t.Fatalf("second start: %v", err)
	}
	done, err := s.stopRecord()
	if err != nil {
		t.Fatal(err)
	}
	if s.state.Recording || done.Path != started.Path || done.Bytes == 0 {
		t.Fatalf("stop: %+v recording=%v", done, s.state.Recording)
	}
	clip, err := os.ReadFile(done.Path)
	if err != nil || !bytes.HasPrefix(clip, []byte("\x89PNG")) {
		t.Fatalf("clip: %v %q", err, clip)
	}
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"-b:v 500k", "-maxrate 500k", "-bufsize 1000k", "-r 10", "drawbox=", "scale=4:4", "libx264"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("ffmpeg args miss %q: %s", want, args)
		}
	}
	if again, err := s.stopRecord(); err != nil || again.Path != done.Path {
		t.Fatalf("stop again: %+v %v", again, err)
	}
}

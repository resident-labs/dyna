package computer

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/computers"
)

func TestStatusExplainsUnavailableDesktopOrStream(t *testing.T) {
	call := rootComputerFixture(t)
	t.Setenv("DYNA_COMPUTERS_TEST_ENABLE", "1")
	t.Setenv("DYNA_COMPUTERS_ENABLED", "")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "labwc"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	socket := computers.ControlSocket("")
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	current := make(chan computers.Status, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- computers.Serve(ctx, listener, func(_ context.Context, r computers.Request) (any, error) {
			if r.Op != "status" {
				return nil, fmt.Errorf("status must not create a desktop: %s", r.Op)
			}
			state := <-current
			state.SessionID = r.Session
			return state, nil
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct {
		name   string
		state  computers.Status
		doctor string
	}{
		{"unavailable", computers.Status{State: computers.StateIdleStopped, Reason: "labwc is not installed"}, "labwc is not installed"},
		{"not started", computers.Status{State: computers.StateCreated}, "desktop not running"},
		{"starting", computers.Status{State: computers.StateStarting}, "desktop starting"},
		{"no graphics", computers.Status{State: computers.StateRunning}, "graphics unavailable"},
		{"no stream", computers.Status{State: computers.StateRunning, Renderer: "gles2"}, "stream not running"},
		{"healthy", computers.Status{State: computers.StateRunning, Renderer: "gles2", Stream: computers.Stream{Running: true, Codec: "h264", Encoder: "ffmpeg"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current <- tc.state
			out, err := call("status")
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			var got struct {
				computers.Status
				Doctor string `json:"doctor"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			if got.Doctor != tc.doctor {
				t.Fatalf("doctor = %q, want %q", got.Doctor, tc.doctor)
			}
			if got.SessionID == "" || got.State != tc.state.State || got.Renderer != tc.state.Renderer || got.Stream != tc.state.Stream {
				t.Fatalf("status details were changed: %+v", got)
			}
		})
	}
}

func TestStatusDoctorReportsMissingWayVNC(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	state := computers.Status{State: computers.StateRunning, Renderer: "gles2"}
	if got, want := statusDoctor(state), "the screen streamer (wayvnc) is not installed"; got != want {
		t.Fatalf("doctor = %q, want %q", got, want)
	}
}

func TestStatusPrintsViewerLinkOnlyWhileRunning(t *testing.T) {
	call := rootComputerFixture(t)
	t.Setenv("DYNA_COMPUTERS_TEST_ENABLE", "1")
	t.Setenv("DYNA_COMPUTERS_ENABLED", "")
	socket := computers.ControlSocket("")
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	current := make(chan computers.Status, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- computers.Serve(ctx, listener, func(_ context.Context, r computers.Request) (any, error) {
			state := <-current
			state.SessionID = r.Session
			return state, nil
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	current <- computers.Status{State: computers.StateRunning}
	out, err := call("status")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Viewer string `json:"viewer"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Viewer, "http://127.0.0.1:7457/#pop=room_") || !strings.Contains(got.Viewer, ":root-room_") {
		t.Fatalf("running computer has no viewer field: %q", out)
	}
	current <- computers.Status{State: computers.StateIdleStopped}
	out, err = call("status")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `"viewer"`) {
		t.Fatalf("stopped computer has a viewer field: %q", out)
	}
}

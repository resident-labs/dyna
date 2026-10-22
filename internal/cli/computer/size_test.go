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

func TestComputerSizeFlagsValidateAndShareResize(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	seen := make(chan string, 20)
	go func() {
		done <- computers.Serve(ctx, listener, func(_ context.Context, r computers.Request) (any, error) {
			seen <- r.Op
			switch r.Op {
			case "register":
				return computers.Meta{SessionID: r.Session, WaylandDisplay: "size-test"}, nil
			case "size":
				data, _ := json.Marshal(r)
				var raw map[string]json.RawMessage
				_ = json.Unmarshal(data, &raw)
				if string(raw["size"]) != `{"width":390,"height":844}` || r.Status != "Checking phone size" {
					return nil, fmt.Errorf("bad resize request: %s", data)
				}
				return computers.Status{Width: 390, Height: 844}, nil
			case "open":
				return computers.Opened{PID: 123, App: computers.App{Title: "Terminal"}}, nil
			default:
				return nil, fmt.Errorf("unexpected operation %s", r.Op)
			}
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	for _, args := range [][]string{
		{"env", "--size", "390x844", "--status", "Checking phone size"},
		{"open", "--size", "390x844", "--status", "Checking phone size", "--", "foot"},
	} {
		if out, err := call(args...); err != nil {
			t.Fatalf("%v: %s %v", args, out, err)
		}
		want := []string{"register", "size"}
		if args[0] == "open" {
			want = append(want, "open")
		}
		for _, op := range want {
			if got := <-seen; got != op {
				t.Fatalf("operation %q, want %q", got, op)
			}
		}
	}
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"env", "--size", "390x844"}, "--status is required"},
		{[]string{"env", "--size", "390"}, "WIDTHxHEIGHT"},
		{[]string{"env", "--size", ""}, "WIDTHxHEIGHT"},
		{[]string{"env", "--size", "390x0"}, "size must be"},
		{[]string{"open", "--size", "99999x844", "--status", "Checking phone size", "--", "foot"}, "size must be"},
	} {
		if _, err := call(tc.args...); err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("%v: %v, want %q", tc.args, err, tc.message)
		}
	}
	select {
	case op := <-seen:
		t.Fatalf("invalid size reached setup: %s", op)
	default:
	}
}

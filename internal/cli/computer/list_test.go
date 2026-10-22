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

func TestListDefaultsToBriefLiveTableAndKeepsExplicitDetail(t *testing.T) {
	call := rootComputerFixture(t)
	values := []computers.Status{
		{SessionID: "live", Room: "alpha", State: computers.StateRunning, Apps: []computers.App{{AppID: "foot", Title: "Terminal"}}, IdleSeconds: 65, Renderer: "renderer-detail", Reason: strings.Repeat("diagnostic detail ", 1024)},
		{SessionID: "starting", Room: "beta", State: computers.StateStarting},
		{SessionID: "created", Room: "alpha", State: computers.StateCreated},
		{SessionID: "stopped", Room: "alpha", State: computers.StateIdleStopped},
		{SessionID: "destroyed", Room: "alpha", State: computers.StateDestroyed},
	}
	byID := map[string]computers.Status{}
	for _, value := range values {
		byID[value.SessionID] = value
		if err := os.MkdirAll(computers.Dir(value.SessionID), 0700); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(computers.Meta{SessionID: value.SessionID, Room: value.Room})
		if err := os.WriteFile(filepath.Join(computers.Dir(value.SessionID), "meta.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
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
	go func() {
		done <- computers.Serve(ctx, listener, func(_ context.Context, r computers.Request) (any, error) {
			if r.Op != "status" {
				return nil, fmt.Errorf("unexpected operation %s", r.Op)
			}
			return byID[r.Session], nil
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	out, err := call("list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "ROOM", "STATE", "APPS", "IDLE", "live", "starting", "foot", "1m5s"} {
		if !strings.Contains(out, want) {
			t.Errorf("table misses %q: %s", want, out)
		}
	}
	for _, old := range []string{"created", "stopped", "destroyed", "renderer-detail", "diagnostic detail"} {
		if strings.Contains(out, old) {
			t.Errorf("default output includes %q", old)
		}
	}
	if len(out) > 500 {
		t.Errorf("default list is verbose: %d bytes", len(out))
	}
	out, err = call("list", "--all")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "idle-stopped") || !strings.Contains(out, "destroyed") {
		t.Fatalf("all table: %s", out)
	}
	for _, all := range []bool{false, true} {
		args := []string{"list", "--json"}
		want := 2
		if all {
			args = append(args, "--all")
			want = 5
		}
		out, err = call(args...)
		if err != nil {
			t.Fatal(err)
		}
		var rows []computers.Status
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != want {
			t.Fatalf("%v: %d rows, want %d", args, len(rows), want)
		}
		found := false
		for _, row := range rows {
			if row.SessionID == "live" {
				found = row.Renderer == "renderer-detail" && row.Reason == values[0].Reason
			}
		}
		if !found {
			t.Fatal("JSON omitted full state")
		}
	}
	out, err = call("list", "--room", "alpha")
	if err != nil || strings.Contains(out, "starting") || !strings.Contains(out, "live") {
		t.Fatalf("room filter: %s %v", out, err)
	}
}

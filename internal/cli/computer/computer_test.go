package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/mode"
	roompkg "dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
)

func TestActionRequiresStatusBeforeComputerSetup(t *testing.T) {
	for _, args := range [][]string{
		{"act", `[{"type":"click","x":1,"y":1,"button":"left"}]`},
		{"act", `[{"type":"type","text":"hello"}]`, "--status", "  "},
		{"stop"},
		{"open", "foot"},
	} {
		cmd := NewCommand()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil || err.Error() != "--status is required: say in a few words what this action is for" {
			t.Fatalf("%v: got %v", args, err)
		}
	}
}

// With no Dyna stamp, the computer belongs to root, the owner of the current
// repo's room. No flag selects another session's computer.
func rootComputerFixture(t *testing.T) func(...string) (string, error) {
	t.Helper()
	storetest.Isolate(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	t.Chdir(repo)
	return func(args ...string) (string, error) {
		cmd := NewCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
}

func TestRootUsesRoomComputerWithoutStamp(t *testing.T) {
	call := rootComputerFixture(t)
	out, err := call("status")
	if err != nil {
		t.Fatalf("root status: %s %v", out, err)
	}
	var st computers.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	rec, err := roompkg.DefaultStore().CurrentRepoRoom()
	if err != nil {
		t.Fatal(err)
	}
	if want := computers.ActorID("", rec.ID); st.SessionID != want {
		t.Fatalf("root computer = %q, want %q", st.SessionID, want)
	}
	if err := computers.ValidateID(st.SessionID); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"status", "--session", "ses_x"}, {"status", "ses_x"}} {
		if _, err := call(args...); err == nil || !strings.Contains(err.Error(), "unknown") && !strings.Contains(err.Error(), "accepts 0 arg") {
			t.Fatalf("%v selected another session: %v", args, err)
		}
	}
}

func TestShotDoesNotHideCaptureFailure(t *testing.T) {
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
	go func() {
		done <- computers.Serve(ctx, listener, func(_ context.Context, r computers.Request) (any, error) {
			switch r.Op {
			case "register":
				return computers.Meta{SessionID: r.Session, Room: r.Room}, nil
			case "shot":
				return nil, fmt.Errorf("capture: output unavailable")
			case "status":
				return computers.Status{SessionID: r.Session, ShotCapturedAt: time.Now().Add(-30 * time.Second)}, nil
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
	if out, err := call("shot"); err == nil || !strings.Contains(err.Error(), "capture: output unavailable") {
		t.Fatalf("capture failure became an old picture: %s %v", out, err)
	}
}

func TestComputerHelpDescribesCommandsAndActions(t *testing.T) {
	for _, command := range NewCommand().Commands() {
		if !command.Hidden && command.Short == "" {
			t.Errorf("%s has no description", command.Name())
		}
	}
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"act", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"click: x y button [keys]", "double_click: x y [keys]", "move: x y", "drag: path [step_ms] [keys]", "pointer_down: x y [button]", "pointer_up: [x y] [button]", "step_ms:",
		"scroll: x y scroll_x scroll_y", "keypress: keys", "type: text [secret]", "wait: [ms]", "ms: integer from 0 to 10000; default 1000 milliseconds.", "screenshot: (no fields)",
		"5 waits", "10 screenshots", "1–50 actions", "2–200", "-3000", "1–12", "10000",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help misses %q", want)
		}
	}
}

func TestOpenForwardsCommandAndWaitsForWindow(t *testing.T) {
	call := rootComputerFixture(t)
	t.Setenv("DYNA_COMPUTERS_TEST_ENABLE", "1")
	// The protocol fixture must work in the plain Go image without GUI tools.
	bin := t.TempDir()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "labwc"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
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
			switch r.Op {
			case "register":
				return computers.Meta{SessionID: r.Session, Room: r.Room}, nil
			case "open":
				data, _ := json.Marshal(r)
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(data, &fields)
				if string(fields["command"]) != `["foot","--title","A title"]` {
					return nil, fmt.Errorf("bad command: %s", data)
				}
				if r.Status != "Checking the terminal" || r.TimeoutMS != 4000 {
					return nil, fmt.Errorf("bad request: %s", data)
				}
				return map[string]any{"pid": 123, "log": "/tmp/app.log", "app": computers.App{AppID: "foot", Title: "A title"}}, nil
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
	out, err := call("open", "--status", "Checking the terminal", "--timeout", "4s", "--", "foot", "--title", "A title")
	if err != nil || !strings.Contains(out, `"title":"A title"`) {
		t.Fatalf("open: %s %v", out, err)
	}
}

func TestOpenRejectsURLAndInvalidTimeoutBeforeSetup(t *testing.T) {
	for _, args := range [][]string{
		{"open", "--status", "Checking a page", "https://example.test"},
		{"open", "--status", "Checking a terminal", "--timeout", "0s", "foot"},
	} {
		cmd := NewCommand()
		cmd.SetArgs(args)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err := cmd.Execute()
		if err == nil || (!strings.Contains(err.Error(), "dyna browser open") && !strings.Contains(err.Error(), "timeout must be positive")) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

// A session stops only its own computer; root and the owner may name any.
func TestStopTargetNamesAnotherComputerOnlyForRootOrOwner(t *testing.T) {
	own := func() (string, string, error) { return "ses_20261020-000000-00000001", "room", nil }
	other := "ses_20261020-000000-00000002"
	storetest.ClearStamp(t)
	if id, err := stopTarget([]string{other}, own); err != nil || id != other {
		t.Fatalf("owner stop = %q, %v", id, err)
	}
	t.Setenv(mode.EnvMode, string(mode.Subagent))
	t.Setenv(mode.EnvSession, "ses_20261020-000000-00000001")
	if _, err := stopTarget([]string{other}, own); err == nil {
		t.Fatal("a session stopped another session's computer")
	}
	if id, err := stopTarget(nil, own); err != nil || id != "ses_20261020-000000-00000001" {
		t.Fatalf("own stop = %q, %v", id, err)
	}
}

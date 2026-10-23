package harness

import (
	"context"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/processguard"
	"dyna-agent/internal/runstore"
)

func TestUnresolvedGrantedPeerCannotRaceReplay(t *testing.T) {
	for _, tc := range []struct {
		name      string
		killErr   error
		wantBack  int
		wantError bool
	}{
		{"retired", nil, 1, false},
		{"retirement fails", errors.New("peer exit unknown"), 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			transport := &hookReceiptTransport{killPeer: func(context.Context, *processguard.Peer) error {
				<-release
				return tc.killErr
			}, connections: map[net.Conn]bool{}}
			hook := &steerHook{transport: transport}
			messages := []runstore.SteeringMessage{{ID: "one", Message: "context"}}
			if err := hook.offer("session", messages, testNow()); err != nil {
				t.Fatal(err)
			}
			server, client := net.Pipe()
			defer client.Close()
			transport.connections[server] = true
			handled := make(chan struct{})
			transport.handlers.Add(1)
			go func() {
				defer transport.handlers.Done()
				defer close(handled)
				defer server.Close()
				transport.serveReceipt(server, &processguard.Peer{})
			}()
			// The hook asked for context and was granted it, then stopped answering.
			go func() { _ = jsonEncode(client, hookRequest{Session: "session"}) }()
			if _, err := jsonDecode(client); err != nil {
				t.Fatal(err)
			}
			if back, delivered := hook.withdraw(); len(back)+len(delivered) != 0 {
				t.Fatalf("replayed granted input: %v %v", back, delivered)
			}
			type outcome struct {
				back, delivered []runstore.SteeringMessage
				err             error
			}
			done := make(chan outcome, 1)
			go func() {
				b, d, err := hook.retire(context.Background())
				done <- outcome{b, d, err}
			}()
			select {
			case got := <-done:
				t.Fatalf("returned before peer retirement: %+v", got)
			default:
			}
			close(release)
			got := <-done
			<-handled
			if (got.err != nil) != tc.wantError || len(got.back) != tc.wantBack || len(got.delivered) != 0 {
				t.Fatalf("outcome = %+v", got)
			}
		})
	}
}

func testNow() time.Time { return time.Time{} }

func jsonEncode(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }

func jsonDecode(r io.Reader) (hookReply, error) {
	var reply hookReply
	err := json.NewDecoder(r).Decode(&reply)
	return reply, err
}

func TestTurnEndReplaysUnclaimedHookOffer(t *testing.T) {
	old := steerHookSupport
	steerHookSupport = func(string, string) bool { return true }
	defer func() { steerHookSupport = old }()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "hold")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "worker.sh")
	body := "printf 'session\\n'\nread -r _ < " + fifo + "\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	offered := make(chan struct{})
	source := &testSource{msgs: []runstore.SteeringMessage{{ID: "m1", Message: "steer"}}}
	supervisor := newSteeringSupervisor(SteeringOptions{Source: source, DeliveryGate: func(offer func() error) error {
		err := offer()
		close(offered)
		return err
	}}, true)
	tracker := newSessionTracker(func(line string) string {
		if strings.TrimSpace(line) == "session" {
			return "s1"
		}
		return ""
	})
	done := make(chan attempt, 1)
	go func() {
		done <- runOnce(t.Context(), profile.Profile{Harness: profile.HarnessMuse, RuntimeExecutable: "/bin/sh", RuntimeBindProcess: func(json.RawMessage) error { return nil }}, dir, commandSpec{prompt: "work", argv: []string{"/bin/sh", script}}, tracker, nil, supervisor, true, "", "", &workeroutput.Declaration{}, nil, newDeliveryGate(JournalOptions{}), &runState{}, ExecutionOptions{SkipComputer: true})
	}()
	select {
	case <-offered:
	case got := <-done:
		t.Fatalf("turn ended before the offer: %v", got.runErr)
	}
	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	got := <-done
	if len(got.steering) != 1 || got.steering[0].ID != "m1" {
		t.Fatalf("continuation messages = %+v, error %v", got.steering, got.runErr)
	}
}

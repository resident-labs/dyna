package harness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
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

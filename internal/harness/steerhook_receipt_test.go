package harness

import (
	"errors"
	"io"
	"net"
	"testing"

	"dyna-agent/internal/runstore"
)

type shortHookWriter struct{ written int }

func (w shortHookWriter) Write(p []byte) (int, error) {
	if w.written < 0 {
		return len(p), nil
	}
	return w.written, nil
}

func TestFailedHookWriteDoesNotAcknowledgeDelivery(t *testing.T) {
	for _, tc := range []struct {
		name                string
		written             int
		reserved, delivered bool
	}{
		{"failed", 0, false, false},
		{"partial", 1, true, false},
		{"complete", -1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &hookReceiptTransport{}
			messages := []runstore.SteeringMessage{{ID: "one", Message: "context"}}
			if err := transport.offer("session", messages); err != nil {
				t.Fatal(err)
			}
			server, client := net.Pipe()
			handled := make(chan struct{})
			go func() { defer close(handled); defer server.Close(); transport.serveReceipt(server, nil) }()
			err := writeHookReceipt(client, "session", shortHookWriter{tc.written}, "PostToolUse")
			client.Close()
			<-handled
			if tc.delivered && err != nil {
				t.Fatal(err)
			}
			if !tc.delivered && !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("write error = %v", err)
			}
			if got := transport.reserved(); got != tc.reserved {
				t.Fatalf("reserved = %v", got)
			}
			accepted := transport.taken()
			if (len(accepted) == 1) != tc.delivered {
				t.Fatalf("acknowledged messages = %+v", accepted)
			}
			back, delivered := transport.withdraw()
			if len(delivered) != 0 {
				t.Fatalf("unexpected delivered messages = %+v", delivered)
			}
			wantBack := !tc.delivered && !tc.reserved
			if (len(back) == 1) != wantBack {
				t.Fatalf("replayable messages = %+v", back)
			}
			if tc.reserved {
				if err := transport.offer("session", messages); err == nil {
					t.Fatal("replaced an unresolved granted offer")
				}
			}
		})
	}
}

package agent

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// engineRefusing answers every call as the engine answers one it refuses.
func engineRefusing(t *testing.T, status int, body string) *Client {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}()
	return NewClient(socket)
}

func TestAQueuedCallCarriesTheEnginesReason(t *testing.T) {
	c := engineRefusing(t, http.StatusBadRequest, `{"code":"invalid","message":"unknown session @ses_1"}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	_, err := c.WorkflowPost(ctx, contract.PostRequest{Text: "x", Mentions: []string{"ses_1"}})
	var api apiError
	if !errors.As(err, &api) || api.code != contract.ErrInvalid || err.Error() != "box API post: 400 Bad Request: unknown session @ses_1" {
		t.Fatalf("%v", err)
	}
	if err := c.WorkflowStepEnd(ctx, contract.WorkflowStepEndRequest{Key: "k"}); err == nil || !strings.Contains(err.Error(), "box API workflow.step_end: 400 Bad Request: unknown session @ses_1") {
		t.Fatalf("%v", err)
	}
}

// The files route and the channel answer with plain text, and a refusal with no body stays its status.
func TestOtherCallsCarryTheReasonToo(t *testing.T) {
	ctx := context.Background()
	c := engineRefusing(t, http.StatusConflict, "room is being deleted\n")
	if err := c.file(ctx, "hash", &bytes.Buffer{}); err == nil || err.Error() != "output file: 409 Conflict: room is being deleted" {
		t.Fatalf("file: %v", err)
	}
	if _, err := c.fetchFile(ctx, contract.TurnFile{SHA256: "hash", Path: filepath.Join(t.TempDir(), "file")}); err == nil || err.Error() != "file download: 409 Conflict: room is being deleted" {
		t.Fatalf("download: %v", err)
	}
	if err := c.Channel(ctx, "binary", func(contract.BoxMessage) error { return nil }, func(bool) {}); err == nil || err.Error() != "box channel: 409 Conflict: room is being deleted" {
		t.Fatalf("channel: %v", err)
	}
	if err := engineRefusing(t, http.StatusConflict, "").file(ctx, "hash", &bytes.Buffer{}); err == nil || err.Error() != "output file: 409 Conflict" {
		t.Fatalf("no body: %v", err)
	}
}

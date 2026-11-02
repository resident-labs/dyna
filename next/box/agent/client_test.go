package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

const engineClosed = "The engine connection closed during this call, so its result is unknown."

func TestAToolCallKnowsTheEngineConnectionClosed(t *testing.T) {
	for name, err := range map[string]error{
		"EOF":            &url.Error{Op: "Post", URL: "http://dyna/v1/hostexec.run", Err: io.EOF},
		"body cut short": io.ErrUnexpectedEOF,
		"reset":          &url.Error{Op: "Post", URL: "http://dyna/v1/post", Err: &net.OpError{Op: "read", Net: "unix", Err: os.NewSyscallError("read", syscall.ECONNRESET)}},
	} {
		if got := callProblem(err); got.Code != contract.ErrUnavailable || got.Message != engineClosed {
			t.Errorf("%s: %+v", name, got)
		}
	}
	other := errors.New("dial unix: no such file or directory")
	if got := callProblem(other); got.Code != contract.ErrUnavailable || got.Message != other.Error() {
		t.Errorf("%+v", got)
	}
}

// The engine drops the socket after it read the call, as a restart does.
func TestForwardReportsASocketClosedDuringTheCall(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}))
	}()
	_, problem := NewClient(socket).forward(context.Background(), contract.MethodPost, []byte(`{}`))
	if problem == nil || problem.Message != engineClosed {
		t.Fatalf("%+v", problem)
	}
}

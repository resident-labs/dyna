package harness

import (
	"context"
	"errors"
	"sync"

	"dyna-agent/internal/harness/sessionhost"
)

// nativeSessionTransport connects an attachment to the shared JSON-RPC client.
// Closing the codec ends its reader; Detach decides the child's lifetime.
type nativeSessionTransport struct {
	session sessionhost.Session
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	err     error
}

func newNativeSessionTransport(session sessionhost.Session) *nativeSessionTransport {
	ctx, cancel := context.WithCancel(context.Background())
	return &nativeSessionTransport{session: session, ctx: ctx, cancel: cancel}
}

func (t *nativeSessionTransport) Read() ([]byte, error) {
	frame, err := t.session.Next(t.ctx)
	if err != nil {
		t.mu.Lock()
		t.err = err
		t.mu.Unlock()
	}
	return frame, err
}

func (t *nativeSessionTransport) Write(frame []byte) error { return t.session.Write(frame) }
func (t *nativeSessionTransport) Close() error             { t.cancel(); return nil }

func (t *nativeSessionTransport) exitError() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	return errors.New("native session connection ended")
}

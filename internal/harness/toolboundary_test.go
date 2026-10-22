package harness

import (
	"sync"
	"time"

	"dyna-agent/internal/runstore"
)

// testSource is an in-memory steering source.
type testSource struct {
	mu     sync.Mutex
	active bool
	msgs   []runstore.SteeringMessage
}

func (m *testSource) Activate() error {
	m.mu.Lock()
	m.active = true
	m.mu.Unlock()
	return nil
}

func (m *testSource) Deactivate() {
	m.mu.Lock()
	m.active = false
	m.mu.Unlock()
}

func (m *testSource) Read(offset int64, final bool) ([]runstore.SteeringMessage, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]runstore.SteeringMessage(nil), m.msgs[offset:]...)
	if final && len(out) == 0 {
		m.active = false
	}
	return out, int64(len(m.msgs)), nil
}

func (m *testSource) submit(msg string) {
	m.mu.Lock()
	m.msgs = append(m.msgs, runstore.SteeringMessage{TS: time.Now().UnixMilli(), Message: msg})
	m.mu.Unlock()
}

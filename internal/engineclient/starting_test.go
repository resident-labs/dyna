package engineclient_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/engineclient"
)

// startingEngine answers the first remaining calls with the startup 503 that
// the engine sends while its migrations run, then answers "ok".
type startingEngine struct {
	remaining, calls int
}

func (e *startingEngine) RoundTrip(*http.Request) (*http.Response, error) {
	e.calls++
	status, body := http.StatusOK, `"ok"`
	if e.remaining > 0 {
		e.remaining--
		status, body = http.StatusServiceUnavailable, `{"error":"the engine is starting","code":"engine_starting"}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// A call made while the engine runs its startup migrations waits and is
// answered once the engine serves calls.
func TestCallWaitsOutAStartingEngine(t *testing.T) {
	at := time.Unix(0, 0)
	var waits []time.Duration
	engine := &startingEngine{remaining: 2}
	client := engineclient.New("", engineclient.RoundTrip(engine), engineclient.FakeClock(&at, &waits), engineclient.Notices(nil))
	var reply string
	if err := client.Call(t.Context(), "Post", nil, &reply); err != nil || reply != "ok" {
		t.Fatalf("call during startup = %q, %v", reply, err)
	}
	if engine.calls != 3 || len(waits) != 2 {
		t.Fatalf("calls = %d, waits = %v; want 3 calls and 2 waits", engine.calls, waits)
	}
}

// A startup that never ends fails once the restart wait is spent, and the
// error keeps the engine's own words.
func TestStuckStartupFailsAfterTheRestartWait(t *testing.T) {
	at := time.Unix(0, 0)
	var waits []time.Duration
	engine := &startingEngine{remaining: 1000}
	client := engineclient.New("", engineclient.RoundTrip(engine), engineclient.FakeClock(&at, &waits), engineclient.Notices(nil))
	err := client.Call(t.Context(), "Post", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "the engine is starting (waited 60 s)") {
		t.Fatalf("stuck startup = %v", err)
	}
	var total time.Duration
	for _, wait := range waits {
		total += wait
	}
	if total != time.Minute {
		t.Fatalf("waited %v in total, want the 1 minute restart wait", total)
	}
}

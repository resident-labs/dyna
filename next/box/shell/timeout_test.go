package shell

import (
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// clockRig hands out each timer by its duration, since a command with a timeout waits on two at once.
type clockRig struct {
	server  *Server
	proc    *fakeProc
	asked   chan wait
	pending map[time.Duration]chan time.Time
	reports chan contract.ShellJobEndedRequest
}

type wait struct {
	d    time.Duration
	fire chan time.Time
}

func newClockRig(output string) *clockRig {
	r := &clockRig{
		proc:    &fakeProc{exit: make(chan int, 1), signals: make(chan syscall.Signal, 4), killOn: syscall.SIGTERM, stopCode: 143},
		asked:   make(chan wait, 8),
		pending: map[time.Duration]chan time.Time{},
		reports: make(chan contract.ShellJobEndedRequest, 4),
	}
	r.server = New(Config{
		After: func(d time.Duration) <-chan time.Time {
			w := wait{d, make(chan time.Time, 1)}
			r.asked <- w
			return w.fire
		},
		newID:  func() contract.ShellJobID { return "job_1" },
		Report: func(req contract.ShellJobEndedRequest) error { r.reports <- req; return nil },
		start: func(_, _, _ string, out io.Writer) (process, error) {
			_, err := io.WriteString(out, output)
			return r.proc, err
		},
	})
	return r
}

// fire waits until something asks for a timer of d and fires it.
func (r *clockRig) fire(t *testing.T, d time.Duration) {
	t.Helper()
	for {
		if c, ok := r.pending[d]; ok {
			delete(r.pending, d)
			c <- time.Time{}
			return
		}
		select {
		case w := <-r.asked:
			r.pending[w.d] = w.fire
		case <-time.After(5 * time.Second):
			t.Fatalf("no timer of %s", d)
		}
	}
}

func TestATimeoutStopsAForegroundCommandLikeJobStop(t *testing.T) {
	r := newClockRig("waiting\n")
	answered := make(chan string)
	go func() {
		_, body := call(t, r.server.Handler(), "/run", `{"command":"make","timeout":20}`)
		answered <- body
	}()
	r.fire(t, 20*time.Second)
	if sig := <-r.proc.signals; sig != syscall.SIGTERM {
		t.Fatalf("signal = %v, want SIGTERM", sig)
	}
	body := <-answered
	if !strings.Contains(body, `"status":"stopped after its 20 s timeout"`) || !strings.Contains(body, `"running":false`) || strings.Contains(body, "exit_code") {
		t.Fatalf("run = %s", body)
	}
	if _, body := call(t, r.server.Handler(), "/job", `{"action":"list"}`); !strings.Contains(body, `"status":"stopped after its 20 s timeout"`) {
		t.Fatalf("list = %s", body)
	}
	select {
	case report := <-r.reports:
		t.Fatalf("a foreground command reported %+v", report)
	case <-time.After(50 * time.Millisecond):
	}
}

// A command with a timeout still moves to the background at the foreground limit, and the timeout stops it there. The session
// wakes, since nobody asked for the stop.
func TestATimeoutStopsABackgroundJobAndWakesTheSession(t *testing.T) {
	r := newClockRig("building\n")
	answered := make(chan string)
	go func() {
		_, body := call(t, r.server.Handler(), "/run", `{"command":"make","timeout":120}`)
		answered <- body
	}()
	r.fire(t, foregroundLimit)
	if body := <-answered; !strings.Contains(body, `"running":true`) {
		t.Fatalf("run = %s", body)
	}
	r.fire(t, 120*time.Second)
	report := <-r.reports
	if report.Job != "job_1" || report.End == nil || report.End.Kind != contract.ShellJobStopped || report.Silent || report.Tail != "building\n[stopped after its 120 s timeout]" {
		t.Fatalf("report = %+v", report)
	}
	if _, body := call(t, r.server.Handler(), "/job", `{"action":"output","job":"job_1"}`); !strings.Contains(body, `"status":"stopped after its 120 s timeout"`) {
		t.Fatalf("output = %s", body)
	}
}

func TestACommandThatEndsBeforeItsTimeoutIsNotStopped(t *testing.T) {
	r := newClockRig("")
	r.proc.exit <- 0
	if _, body := call(t, r.server.Handler(), "/run", `{"command":"true","timeout":120}`); !strings.Contains(body, `"status":"exit 0"`) {
		t.Fatalf("run = %s", body)
	}
	r.fire(t, 120*time.Second)
	select {
	case sig := <-r.proc.signals:
		t.Fatalf("a finished command got %v", sig)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTimeoutIsSecondsUpToTwoHours(t *testing.T) {
	r := newRig("", syscall.SIGKILL)
	for _, timeout := range []string{"-1", "7201"} {
		status, body := call(t, r.server.Handler(), "/run", `{"command":"true","timeout":`+timeout+`}`)
		if status != http.StatusBadRequest || !strings.Contains(body, "timeout is in seconds, at most 7200") {
			t.Fatalf("timeout %s = %d %s", timeout, status, body)
		}
	}
}

func TestATimeoutStopsARealCommand(t *testing.T) {
	server := New(Config{After: time.After})
	defer server.Close()
	started := time.Now()
	status, body := call(t, server.Handler(), "/run", `{"command":"echo begun; sleep 20","timeout":1}`)
	if status != http.StatusOK || !strings.Contains(body, "begun") || !strings.Contains(body, `"status":"stopped after its 1 s timeout"`) {
		t.Fatalf("run = %d %s", status, body)
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Fatalf("the command ran %s", took)
	}
}

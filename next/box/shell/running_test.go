package shell

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// statusRig runs one job under a clock the test moves by hand.
type statusRig struct {
	server   *Server
	proc     *fakeProc
	out      chan io.Writer
	waits    chan statusWait
	statuses chan contract.ShellJobRunningRequest
	mu       sync.Mutex
	at       time.Time
}

type statusWait struct {
	wait time.Duration
	fire chan time.Time
}

func (r *statusRig) now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.at
}

func (r *statusRig) move(by time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.at = r.at.Add(by)
}

func newStatusRig(t *testing.T) (*statusRig, io.Writer, *job) {
	t.Helper()
	r := &statusRig{
		proc:     &fakeProc{exit: make(chan int, 1), signals: make(chan syscall.Signal, 4), killOn: syscall.SIGTERM},
		out:      make(chan io.Writer, 1),
		waits:    make(chan statusWait, 4),
		statuses: make(chan contract.ShellJobRunningRequest, 8),
		at:       time.Unix(1000, 0),
	}
	r.server = New(Config{
		After: func(d time.Duration) <-chan time.Time {
			w := statusWait{d, make(chan time.Time, 1)}
			r.waits <- w
			return w.fire
		},
		newID:  func() contract.ShellJobID { return "job_1" },
		now:    r.now,
		Report: func(contract.ShellJobEndedRequest) error { return nil },
		Running: func(req contract.ShellJobRunningRequest) error {
			r.statuses <- req
			return nil
		},
		start: func(_, _, _ string, out io.Writer) (process, error) {
			r.out <- out
			return r.proc, nil
		},
	})
	t.Cleanup(func() {
		select {
		case r.proc.exit <- 0:
		default:
		}
	})
	done := make(chan *job)
	go func() {
		j, _ := r.server.run(nil, Run{Command: "make", Description: "Build"})
		done <- j
	}()
	out := <-r.out
	r.nextWait(t).fire <- time.Time{} // the foreground limit
	return r, out, <-done
}

func (r *statusRig) nextWait(t *testing.T) statusWait {
	t.Helper()
	select {
	case w := <-r.waits:
		return w
	case <-time.After(5 * time.Second):
		t.Fatal("no timer was set")
		return statusWait{}
	}
}

func (r *statusRig) status(t *testing.T) contract.ShellJobRunningRequest {
	t.Helper()
	select {
	case req := <-r.statuses:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("no status")
		return contract.ShellJobRunningRequest{}
	}
}

// reach moves the clock to the next mark, as the timer asks, and takes the status.
func (r *statusRig) reach(t *testing.T) contract.ShellJobRunningRequest {
	t.Helper()
	w := r.nextWait(t)
	r.move(w.wait)
	w.fire <- time.Time{}
	return r.status(t)
}

func TestRunningJobReportsAt15And30And60ThenEvery60(t *testing.T) {
	r, _, _ := newStatusRig(t)
	for _, minutes := range []int{15, 30, 60, 120, 180, 240} {
		if got := r.reach(t); got.RanMinutes != minutes {
			t.Fatalf("status at %d minutes, want %d", got.RanMinutes, minutes)
		}
	}
}

func TestNextMarkFromAnyRunningTime(t *testing.T) {
	for ran, want := range map[time.Duration]time.Duration{
		30 * time.Second: 15 * time.Minute,
		15 * time.Minute: 30 * time.Minute,
		29 * time.Minute: 30 * time.Minute,
		30 * time.Minute: time.Hour,
		time.Hour:        2 * time.Hour,
		90 * time.Minute: 2 * time.Hour,
		5 * time.Hour:    6 * time.Hour,
	} {
		if got := nextMark(ran); got != want {
			t.Errorf("after %s: %s, want %s", ran, got, want)
		}
	}
}

func TestStatusStatesTimesAndLastLines(t *testing.T) {
	r, out, _ := newStatusRig(t)
	var lines []string
	for i := 1; i <= 8; i++ {
		lines = append(lines, fmt.Sprintf("line %d\n", i))
	}
	w := r.nextWait(t)
	r.move(3 * time.Minute)
	if _, err := io.WriteString(out, strings.Join(lines, "")); err != nil {
		t.Fatal(err)
	}
	r.move(w.wait - 3*time.Minute)
	w.fire <- time.Time{}
	got := r.status(t) // written three minutes in, so twelve minutes quiet at the mark
	if got.Job != "job_1" || got.Description != "Build" || got.QuietMinutes == nil || *got.QuietMinutes != 12 {
		t.Fatalf("status %+v", got)
	}
	if want := "line 4\nline 5\nline 6\nline 7\nline 8"; got.Tail != want {
		t.Fatalf("tail=%q, want %q", got.Tail, want)
	}
}

func TestStatusWithNoOutputSaysSo(t *testing.T) {
	r, _, _ := newStatusRig(t)
	if got := r.reach(t); got.QuietMinutes != nil || got.Tail != "" {
		t.Fatalf("status %+v", got)
	}
}

func TestStatusMasksAndHoldsBackAnUnfinishedLine(t *testing.T) {
	r, out, _ := newStatusRig(t)
	token := "ghp_" + strings.Repeat("a1", 15)
	// The first token is whole and masked. The second may go on in the next write, so its line waits.
	if _, err := io.WriteString(out, "first "+token+" done\nsecond "+token[:12]); err != nil {
		t.Fatal(err)
	}
	got := r.reach(t)
	if got.Tail != "first [masked] done" {
		t.Fatalf("tail=%q", got.Tail)
	}
	if _, err := io.WriteString(out, token[12:]+"\n"); err != nil {
		t.Fatal(err)
	}
	got = r.reach(t)
	if strings.Contains(got.Tail, "a1a1") || got.Tail != "first [masked] done\nsecond [masked]" {
		t.Fatalf("tail=%q", got.Tail)
	}
}

func TestLongOutputKeepsItsNewestBytesInTheStatus(t *testing.T) {
	r, out, _ := newStatusRig(t)
	if _, err := io.WriteString(out, strings.Repeat("é", tailLimit)+"\n"); err != nil {
		t.Fatal(err)
	}
	got := r.reach(t)
	if len(got.Tail) > tailLimit || strings.ContainsRune(got.Tail, '�') || !strings.HasSuffix(got.Tail, "é") {
		t.Fatalf("%d bytes, ends %q", len(got.Tail), got.Tail[len(got.Tail)-8:])
	}
}

func TestEndedJobSendsNoFurtherStatus(t *testing.T) {
	r, _, j := newStatusRig(t)
	r.reach(t)
	w := r.nextWait(t)
	r.proc.exit <- 0
	<-j.done
	r.move(w.wait)
	w.fire <- time.Time{}
	select {
	case got := <-r.statuses:
		t.Fatalf("status after the job ended: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

// The mark falls in the moment the job ends, before its done channel closes.
func TestJobEndingAtTheMarkSendsNoStatus(t *testing.T) {
	r, _, j := newStatusRig(t)
	r.reach(t)
	w := r.nextWait(t)
	j.mu.Lock()
	j.finished = true
	j.mu.Unlock()
	r.move(w.wait)
	w.fire <- time.Time{}
	select {
	case got := <-r.statuses:
		t.Fatalf("status from a job that ended: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

// The mark falls while the stop is still taking the group down.
func TestStoppedJobSendsNoFurtherStatus(t *testing.T) {
	r, _, j := newStatusRig(t)
	r.reach(t)
	w := r.nextWait(t)
	j.mu.Lock()
	j.stopped = true
	j.mu.Unlock()
	r.move(w.wait)
	w.fire <- time.Time{}
	select {
	case got := <-r.statuses:
		t.Fatalf("status after a stop: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestForegroundJobHasNoStatus(t *testing.T) {
	r := newRig("quick\n", syscall.SIGKILL)
	statuses := make(chan contract.ShellJobRunningRequest, 1)
	r.server.cfg.Running = func(req contract.ShellJobRunningRequest) error {
		statuses <- req
		return nil
	}
	r.proc.exit <- 0
	if _, err := r.server.run(nil, Run{Command: "true"}); err != nil {
		t.Fatal(err)
	}
	if len(r.timers) > 1 {
		t.Fatalf("%d timers for a job that ended in the foreground", len(r.timers))
	}
	if len(statuses) != 0 {
		t.Fatal("a foreground job sent a status")
	}
}

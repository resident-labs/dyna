package shell

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestOutputReaderTurnBoundaryAndCompletion(t *testing.T) {
	timers := make(chan chan time.Time, 20)
	s := New(Config{After: func(time.Duration) <-chan time.Time { ch := make(chan time.Time, 1); timers <- ch; return ch }})
	j := &job{id: "job_111111111111", done: make(chan struct{}), now: time.Now}
	other := &job{id: "job_222222222222", done: make(chan struct{}), now: time.Now}
	s.jobs = []*job{j, other}
	var reader OutputReader
	reader.Reset()
	check := func(r *OutputReader, id contract.ShellJobID, wait bool) Result {
		t.Helper()
		done := make(chan Result, 1)
		go func() {
			a, err := r.Job(s, JobCall{Action: "output", Job: id})
			if err != nil {
				t.Error(err)
				done <- Result{}
				return
			}
			done <- a.(Result)
		}()
		if wait {
			select {
			case ch := <-timers:
				ch <- time.Time{}
			case <-done:
				t.Fatal("first output did not wait")
			case <-time.After(time.Second):
				t.Fatal("no wait timer")
			}
			// awaitOutput also registers its output-poll timer before selecting.
			select {
			case <-timers:
			case <-time.After(time.Second):
				t.Fatal("no poll timer")
			}
		}
		select {
		case a := <-done:
			return a
		case <-time.After(time.Second):
			t.Fatal("output did not return promptly")
			return Result{}
		}
	}
	if a := check(&reader, j.id, true); !a.Running || a.Repeated {
		t.Fatalf("first: %+v", a)
	}
	if a := check(&reader, j.id, false); !a.Running || !a.Repeated {
		t.Fatalf("repeat: %+v", a)
	}
	check(&reader, other.id, true)
	var worker OutputReader
	check(&worker, j.id, true)
	reader.Reset()
	if a := check(&reader, j.id, true); a.Repeated {
		t.Fatal("new turn inherited check")
	}
	j.Write([]byte("last output\n"))
	j.mu.Lock()
	j.finished = true
	j.end = &contract.ShellJobEnd{Kind: contract.ShellJobExited}
	close(j.done)
	j.mu.Unlock()
	if a := check(&reader, j.id, false); a.Running || a.Repeated || a.Output != "last output\n" {
		t.Fatalf("completion lost: %+v", a)
	}
}

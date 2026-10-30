package shell

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type fakeProc struct {
	exit    chan int
	signals chan syscall.Signal
	killOn  syscall.Signal // the signal that ends the process
	orphan  bool           // SIGTERM ends the leader, but a child survives until SIGKILL
}

func (p *fakeProc) Wait() int        { return <-p.exit }
func (p *fakeProc) GroupAlive() bool { return p.orphan }
func (p *fakeProc) Signal(sig syscall.Signal) {
	p.signals <- sig
	if sig == syscall.SIGTERM && p.orphan {
		p.exit <- 0
	}
	if sig == syscall.SIGKILL {
		p.orphan = false
	}
	if sig == p.killOn {
		p.exit <- 137
	}
}

type timer struct{ fire chan time.Time }

type rig struct {
	server  *Server
	proc    *fakeProc
	timers  chan timer
	reports chan contract.ShellJobEndedRequest
	failing int // reports that fail before one succeeds
}

func newRig(output string, killOn syscall.Signal) *rig {
	r := &rig{
		proc:    &fakeProc{exit: make(chan int, 1), signals: make(chan syscall.Signal, 4), killOn: killOn},
		timers:  make(chan timer, 4),
		reports: make(chan contract.ShellJobEndedRequest, 4),
	}
	r.server = New(Config{
		Dir: "/box/work",
		After: func(time.Duration) <-chan time.Time {
			t := timer{make(chan time.Time, 1)}
			r.timers <- t
			return t.fire
		},
		newID: func() contract.ShellJobID { return "job_1" },
		Report: func(req contract.ShellJobEndedRequest) error {
			r.reports <- req
			if r.failing > 0 {
				r.failing--
				return errors.New("engine unreachable")
			}
			return nil
		},
		start: func(_, _ string, out io.Writer) (process, error) {
			_, err := io.WriteString(out, output)
			return r.proc, err
		},
	})
	return r
}

func TestSlowCommandMovesToBackgroundAndReportsItsEnd(t *testing.T) {
	r := newRig("building\n", syscall.SIGKILL)
	done := make(chan *job)
	go func() {
		j, _ := r.server.run(nil, "make", "")
		done <- j
	}()
	(<-r.timers).fire <- time.Time{}
	j := <-done
	if got := describe(j); !strings.Contains(got, "running in the background") || !strings.Contains(got, "building") {
		t.Fatalf("unexpected foreground result %q", got)
	}
	if r.server.Running() != 1 {
		t.Fatalf("running = %d, want 1", r.server.Running())
	}
	r.proc.exit <- 3
	report := <-r.reports
	if report.Job != "job_1" || report.ExitCode != 3 || report.Tail != "building\n" {
		t.Fatalf("unexpected report %+v", report)
	}
	<-j.done
	if r.server.Running() != 0 {
		t.Fatalf("running = %d, want 0", r.server.Running())
	}
}

func TestFastCommandReturnsItsExitWithoutReport(t *testing.T) {
	r := newRig("ok\n", syscall.SIGKILL)
	r.proc.exit <- 0
	j, err := r.server.run(nil, "true", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := describe(j); got != "ok\n\n[job job_1: exit 0]" {
		t.Fatalf("unexpected result %q", got)
	}
	select {
	case report := <-r.reports:
		t.Fatalf("unexpected report %+v", report)
	default:
	}
}

func TestStopKillsTheGroupAfterTheGracePeriod(t *testing.T) {
	r := newRig("", syscall.SIGKILL) // the process ignores SIGTERM
	done := make(chan *job)
	go func() {
		j, _ := r.server.run(nil, "sleep 99", "")
		done <- j
	}()
	(<-r.timers).fire <- time.Time{}
	j := <-done
	stopped := make(chan struct{})
	go func() { r.server.stop(j); close(stopped) }()
	if sig := <-r.proc.signals; sig != syscall.SIGTERM {
		t.Fatalf("first signal = %v, want SIGTERM", sig)
	}
	(<-r.timers).fire <- time.Time{}
	if sig := <-r.proc.signals; sig != syscall.SIGKILL {
		t.Fatalf("second signal = %v, want SIGKILL", sig)
	}
	<-stopped
	select {
	case report := <-r.reports:
		t.Fatalf("a stopped job must not report: %+v", report)
	default:
	}
}

func TestCloseKillsEveryJobGroup(t *testing.T) {
	r := newRig("", syscall.SIGKILL)
	done := make(chan *job)
	go func() {
		j, _ := r.server.run(nil, "sleep 99", "")
		done <- j
	}()
	(<-r.timers).fire <- time.Time{}
	<-done
	r.server.Close()
	if sig := <-r.proc.signals; sig != syscall.SIGKILL {
		t.Fatalf("signal = %v, want SIGKILL", sig)
	}
}

func TestMaskHidesSecretsOnly(t *testing.T) {
	for text, secret := range map[string]string{
		"ghp_" + strings.Repeat("a1", 15):                              "a1a1a1",
		"github_pat_" + strings.Repeat("B2", 15):                       "B2B2B2",
		"sk-" + strings.Repeat("c3", 15):                               "c3c3c3",
		"xoxb-1234567890-abcdef":                                       "1234567890",
		"AKIAIOSFODNN7EXAMPLE":                                         "IOSFODNN7",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijk": "abcdefghijk",
		"DATABASE_PASSWORD=hunter2":                                    "hunter2",
		`"api_key": "abc123"`:                                          "abc123",
		"token: 9f86d081884c7d659a2feaa0c55ad015":                      "9f86d081",
	} {
		got := Mask("before " + text + "\nafter")
		if strings.Contains(got, secret) || !strings.Contains(got, masked) || !strings.HasSuffix(got, "\nafter") {
			t.Errorf("secret %q not masked: %q", text, got)
		}
	}
	for _, text := range []string{"ok: build passed in 3 s", "keyboard: us", "monkey=1", "see task-force-delta-seven-seven-seven", "PATH=/usr/bin"} {
		if got := Mask(text); got != text {
			t.Errorf("ordinary text changed: %q -> %q", text, got)
		}
	}
}

func TestHandlerRunsAShellCall(t *testing.T) {
	r := newRig("secret=abc\n", syscall.SIGKILL)
	r.proc.exit <- 0
	server := httptest.NewServer(r.server.Handler())
	defer server.Close()
	post := func(body string) (*http.Response, string) {
		resp, err := http.Post(server.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp, string(data)
	}
	if resp, _ := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification status = %d", resp.StatusCode)
	}
	if _, body := post(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); !strings.Contains(body, `"shell_jobs"`) {
		t.Fatalf("tools/list = %s", body)
	}
	_, body := post(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"shell","arguments":{"command":"env"}}}`)
	if !strings.Contains(body, "secret=[masked]") || strings.Contains(body, "abc") || !strings.Contains(body, "exit 0") {
		t.Fatalf("tools/call = %s", body)
	}
}

func TestReportRetriesUntilTheEngineAccepts(t *testing.T) {
	r := newRig("", syscall.SIGKILL)
	r.failing = 1
	done := make(chan *job)
	go func() {
		j, _ := r.server.run(nil, "make", "")
		done <- j
	}()
	(<-r.timers).fire <- time.Time{}
	<-done
	r.proc.exit <- 0
	<-r.reports // the first report fails
	(<-r.timers).fire <- time.Time{}
	if report := <-r.reports; report.Job != "job_1" {
		t.Fatalf("unexpected retry %+v", report)
	}
}

func TestOnlyTheLastFinishedJobsAreKept(t *testing.T) {
	n := 0
	server := New(Config{
		After: func(time.Duration) <-chan time.Time { return nil },
		newID: func() contract.ShellJobID { n++; return contract.ShellJobID(fmt.Sprintf("job_%d", n)) },
		start: func(string, string, io.Writer) (process, error) {
			p := &fakeProc{exit: make(chan int, 1)}
			p.exit <- 0
			return p, nil
		},
	})
	for range keepFinished + 2 {
		if _, err := server.run(nil, "true", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := server.find("job_1"); ok {
		t.Fatal("the oldest finished job is still kept")
	}
	if _, ok := server.find("job_2"); !ok {
		t.Fatal("job_2 was dropped too early")
	}
	if len(server.jobs) != keepFinished+1 {
		t.Fatalf("kept %d jobs, want %d", len(server.jobs), keepFinished+1)
	}
}

func TestTokenSplitAcrossWritesIsMasked(t *testing.T) {
	token := "ghp_" + strings.Repeat("a1", 15)
	j := &job{}
	_, _ = j.Write([]byte("start " + token[:10]))
	first, _ := j.unread()
	_, _ = j.Write([]byte(token[10:] + " end\n"))
	second, _ := j.unread()
	if strings.Contains(first+second, "a1a1") || first != "" || second != "start [masked] end\n" {
		t.Fatalf("leaked or wrong split: %q %q", first, second)
	}
}

func TestStopKillsAChildThatOutlivesTheLeader(t *testing.T) {
	r := newRig("", 0)
	r.proc.orphan = true
	done := make(chan *job)
	go func() {
		j, _ := r.server.run(nil, "sleep 99 &", "")
		done <- j
	}()
	(<-r.timers).fire <- time.Time{}
	j := <-done
	go r.server.stop(j)
	if sig := <-r.proc.signals; sig != syscall.SIGTERM {
		t.Fatalf("first signal = %v, want SIGTERM", sig)
	}
	(<-r.timers).fire <- time.Time{}
	if sig := <-r.proc.signals; sig != syscall.SIGKILL {
		t.Fatalf("second signal = %v, want SIGKILL", sig)
	}
}

func TestReportTailMasksATokenAcrossTheCut(t *testing.T) {
	token := "ghp_" + strings.Repeat("a1", 15)
	r := newRig(strings.Repeat("x", tailLimit-10)+" "+token+"\n", syscall.SIGKILL)
	r.proc.exit <- 0
	if _, err := r.server.run(nil, "env", ""); err != nil {
		t.Fatal(err)
	}
	j, _ := r.server.find("job_1")
	j.mu.Lock()
	j.background = true
	j.mu.Unlock()
	r.server.report(j)
	if tail := (<-r.reports).Tail; strings.Contains(tail, "a1a1") || !strings.Contains(tail, masked) {
		t.Fatalf("tail leaks or lost the mask: %q", tail[len(tail)-60:])
	}
}

func TestOutputKeepsTheNewestBytesAndReportsTheDrop(t *testing.T) {
	j := &job{finished: true}
	line := strings.Repeat("y", 99) + "\n"
	for range 2*outputLimit/len(line) + 10 {
		_, _ = j.Write([]byte(line))
	}
	_, _ = j.Write([]byte("last\n"))
	text, _ := j.unread()
	if !strings.HasPrefix(text, "[") || !strings.Contains(text, "bytes of earlier output were dropped]") || !strings.HasSuffix(text, "last\n") || len(j.output) > 2*outputLimit {
		t.Fatalf("unexpected output of %d bytes, starting %q", len(text), text[:60])
	}
}

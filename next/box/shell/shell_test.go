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
	exit     chan int
	signals  chan syscall.Signal
	signal   string
	stopCode int
	killOn   syscall.Signal // the signal that ends the process
	orphan   bool           // SIGTERM ends the leader, but a child survives until SIGKILL
}

func (p *fakeProc) Wait() (int, string) { return <-p.exit, p.signal }
func (p *fakeProc) GroupAlive() bool    { return p.orphan }
func (p *fakeProc) Signal(sig syscall.Signal) {
	p.signals <- sig
	if sig == syscall.SIGTERM && p.orphan {
		p.exit <- 0
	}
	if sig == syscall.SIGKILL {
		p.orphan = false
	}
	if sig == p.killOn {
		p.exit <- p.stopCode
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
		proc:    &fakeProc{exit: make(chan int, 1), signals: make(chan syscall.Signal, 4), killOn: killOn, stopCode: 137},
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
		start: func(_, _, _ string, out io.Writer) (process, error) {
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
		j, _ := r.server.run(nil, "make", "", "", "")
		done <- j
	}()
	(<-r.timers).fire <- time.Time{}
	j := <-done
	if got := describe(j); !strings.Contains(got, "running in the background") || !strings.Contains(got, "building") {
		t.Fatalf("unexpected foreground result %q", got)
	}
	if len(r.server.Running()) != 1 {
		t.Fatalf("running = %v, want 1", r.server.Running())
	}
	r.proc.exit <- 3
	report := <-r.reports
	if report.Job != "job_1" || report.ExitCode != 3 || report.Tail != "building\n" {
		t.Fatalf("unexpected report %+v", report)
	}
	<-j.done
	if len(r.server.Running()) != 0 {
		t.Fatalf("running = %v, want 0", r.server.Running())
	}
}

func TestFastCommandReturnsItsExitWithoutReport(t *testing.T) {
	r := newRig("ok\n", syscall.SIGKILL)
	r.proc.exit <- 0
	j, err := r.server.run(nil, "true", "", "", "")
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
		j, _ := r.server.run(nil, "sleep 99", "", "", "")
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
		t.Fatalf("stopped job reported: %+v", report)
	default:
	}
}

func TestCloseKillsEveryJobGroup(t *testing.T) {
	r := newRig("", syscall.SIGKILL)
	done := make(chan *job)
	go func() {
		j, _ := r.server.run(nil, "sleep 99", "", "", "")
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
		if strings.Contains(got, secret) || !strings.Contains(got, "[masked]") || !strings.HasSuffix(got, "\nafter") {
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

func TestAWorkersShellStartsInItsOwnTree(t *testing.T) {
	for cwd, want := range map[string]string{"": "/box/work/repo.worktrees/one", "src": "/box/work/repo.worktrees/one/src", "/tmp": "/tmp"} {
		r := newRig("", syscall.SIGKILL)
		var got string
		r.server.cfg.start = func(_, dir, _ string, _ io.Writer) (process, error) {
			got = dir
			return r.proc, nil
		}
		r.proc.exit <- 0
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"shell","arguments":{"command":"pwd","cwd":"` + cwd + `"}}}`
		r.server.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/?dir=/box/work/repo.worktrees/one", strings.NewReader(body)))
		if got != want {
			t.Fatalf("cwd %q: started in %q, want %q", cwd, got, want)
		}
	}
}

func TestMCPPassesCallIDToShell(t *testing.T) {
	r := newRig("", syscall.SIGKILL)
	var got string
	r.server.cfg.start = func(_, _, id string, _ io.Writer) (process, error) {
		got = id
		return r.proc, nil
	}
	r.proc.exit <- 0
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"shell","arguments":{"command":"true","_dynaCallId":"call-1"}}}`))
	response := httptest.NewRecorder()
	r.server.Handler().ServeHTTP(response, request)
	if got != "call-1" {
		t.Fatalf("call ID = %q", got)
	}
}

func TestReportRetriesUntilTheEngineAccepts(t *testing.T) {
	r := newRig("", syscall.SIGKILL)
	r.failing = 1
	done := make(chan *job)
	go func() {
		j, _ := r.server.run(nil, "make", "", "", "")
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
		start: func(string, string, string, io.Writer) (process, error) {
			p := &fakeProc{exit: make(chan int, 1)}
			p.exit <- 0
			return p, nil
		},
	})
	for range keepFinished + 2 {
		if _, err := server.run(nil, "true", "", "", ""); err != nil {
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
		j, _ := r.server.run(nil, "sleep 99 &", "", "", "")
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
	if _, err := r.server.run(nil, "env", "", "", ""); err != nil {
		t.Fatal(err)
	}
	j, _ := r.server.find("job_1")
	j.mu.Lock()
	j.background = true
	j.mu.Unlock()
	r.server.report(j)
	if tail := (<-r.reports).Tail; strings.Contains(tail, "a1a1") || !strings.Contains(tail, "[masked]") {
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

func TestSecretCrossingTheTrimPointWithoutNewlineIsDropped(t *testing.T) {
	token := "ghp_" + strings.Repeat("a1", 15)
	j := &job{}
	_, _ = j.Write([]byte(strings.Repeat("x", 2*outputLimit-5) + token))
	_, _ = j.Write([]byte(strings.Repeat("a1", 10) + "\nlast\n"))
	j.finished = true
	text, _ := j.unread()
	if strings.Contains(text, "a1") || !strings.HasSuffix(text, "last\n") || !strings.Contains(text, "bytes of earlier output were dropped]") {
		t.Fatalf("leaked or wrong text: %q", text[:min(len(text), 80)])
	}
}

func TestOwnerViewKeepsAgentPosition(t *testing.T) {
	token := "ghp_" + strings.Repeat("a1", 15)
	j := &job{id: "j", command: "build " + token, done: make(chan struct{})}
	s := New(Config{})
	s.jobs = []*job{j}
	j.Write([]byte("first\npartial"))
	first := s.View(true)
	if len(first) != 1 || !first[0].Reset || first[0].Text != "first\n" || strings.Contains(first[0].Command, token) {
		t.Fatalf("reset: %+v", first)
	}
	if text, _ := j.unread(); text != "first\n" {
		t.Fatalf("agent read moved: %q", text)
	}
	j.Write([]byte(" line\n"))
	next := s.View(false)
	if len(next) != 1 || next[0].Reset || next[0].Text != "partial line\n" {
		t.Fatalf("delta: %+v", next)
	}
	j.Write([]byte(token + "\n"))
	masked := s.View(false)
	if len(masked) != 1 || strings.Contains(masked[0].Text, token) {
		t.Fatalf("unmasked: %+v", masked)
	}
	j.mu.Lock()
	j.finished = true
	j.exit = 7
	j.mu.Unlock()
	next = s.View(false)
	if len(next) != 1 || next[0].Running || next[0].ExitCode != 7 {
		t.Fatalf("state: %+v", next)
	}
}

func TestOwnerViewCarriesJobTimes(t *testing.T) {
	r := newRig("ok\n", syscall.SIGKILL)
	started, ended := time.Unix(100, 0), time.Unix(138, 0)
	times := []time.Time{started, ended}
	r.server.cfg.now = func() time.Time { now := times[0]; times = times[1:]; return now }
	r.proc.exit <- 0
	j, err := r.server.run(nil, "make", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	<-j.done
	if view := r.server.View(true); len(view) != 1 || !view[0].Started.Equal(started) || !view[0].Ended.Equal(ended) {
		t.Fatalf("times: %+v", view)
	}
}

func TestOwnerViewBudgetsAndResets(t *testing.T) {
	s := New(Config{})
	for i := 0; i < 8; i++ {
		j := &job{id: contract.ShellJobID(fmt.Sprint(i)), done: make(chan struct{})}
		j.Write([]byte(strings.Repeat("x", (64<<10)-1) + "\n"))
		s.jobs = append(s.jobs, j)
	}
	first := s.View(true)
	if len(first) != 4 {
		t.Fatalf("first batch: %d", len(first))
	}
	second := s.View(false)
	if len(second) != 4 {
		t.Fatalf("second batch: %d", len(second))
	}
	for _, j := range append(first, second...) {
		if !j.Reset || len(j.Text) > 64<<10 {
			t.Fatalf("job: %+v", j)
		}
	}
	s.jobs[0].Write([]byte(strings.Repeat("x\n", 1<<19)))
	third := s.View(false)
	if len(third) != 1 || !third[0].Reset || len(third[0].Text) > 64<<10 {
		t.Fatalf("large output: %+v", third)
	}
	s.jobs = nil
	for i := 0; i < 70; i++ {
		s.jobs = append(s.jobs, &job{id: contract.ShellJobID(fmt.Sprint(i)), command: strings.Repeat("é", 3000), done: make(chan struct{})})
	}
	commands := s.View(true)
	if len(commands) != 61 || len(commands[0].Command) != 4096 {
		t.Fatalf("command batch: %d", len(commands))
	}
	if rest := s.View(false); len(rest) != 9 || !rest[0].Reset {
		t.Fatalf("remaining commands: %d", len(rest))
	}
}

func TestJobEndAndDescription(t *testing.T) {
	for _, tc := range []struct {
		name, signal string
		code         int
		stopped      bool
		want         contract.ShellJobEnd
	}{
		{name: "exit", code: 2, want: contract.ShellJobEnd{Kind: contract.ShellJobExited, Code: 2}},
		{name: "signal", code: -1, signal: "SIGTERM", want: contract.ShellJobEnd{Kind: contract.ShellJobSignaled, Signal: "SIGTERM"}},
		{name: "stopped with signal", code: -1, stopped: true, want: contract.ShellJobEnd{Kind: contract.ShellJobStopped}},
		{name: "stopped with exit 0", code: 0, stopped: true, want: contract.ShellJobEnd{Kind: contract.ShellJobStopped}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig("", syscall.SIGTERM)
			r.proc.signal = tc.signal
			r.proc.stopCode = tc.code
			done := make(chan *job, 1)
			go func() { j, _ := r.server.run(nil, "build", "", "Build the bundle", ""); done <- j }()
			(<-r.timers).fire <- time.Time{}
			j := <-done
			if tc.stopped {
				r.server.stop(j)
			} else {
				r.proc.exit <- tc.code
			}
			<-j.done
			view := r.server.View(true)
			if len(view) != 1 || view[0].Description != "Build the bundle" || *view[0].End != tc.want {
				t.Fatalf("view=%+v", view)
			}
			if !tc.stopped {
				report := <-r.reports
				if report.Description != view[0].Description || *report.End != tc.want {
					t.Fatalf("report=%+v", report)
				}
			}
		})
	}
}

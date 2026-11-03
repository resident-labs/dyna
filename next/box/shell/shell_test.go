package shell

import (
	"context"
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
		j, _ := r.server.run(nil, Run{Command: "make"})
		done <- j
	}()
	(<-r.timers).fire <- time.Time{}
	j := <-done
	if got := result(j); !got.Running || got.Status != "running" || got.ExitCode != nil || !strings.Contains(got.Output, "building") {
		t.Fatalf("unexpected foreground result %+v", got)
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

// A message for the session ends the foreground wait at once, as the limit would. The job keeps running and reports its end,
// and a later message leaves it alone.
func TestAMessageMovesAForegroundCommandToTheBackground(t *testing.T) {
	r := newRig("building\n", syscall.SIGKILL)
	answered := make(chan string)
	go func() {
		_, body := call(t, r.server.Handler(), "/run", `{"command":"make"}`)
		answered <- body
	}()
	<-r.timers // the command waits in the foreground
	r.server.StepAside()
	if body := <-answered; !strings.Contains(body, `"running":true`) || !strings.Contains(body, `"message_arrived":true`) || !strings.Contains(body, "building") {
		t.Fatalf("run = %s", body)
	}
	r.server.StepAside()
	if running := r.server.Running(); len(running) != 1 {
		t.Fatalf("running = %v, want the moved job", running)
	}
	r.proc.exit <- 3
	if report := <-r.reports; report.Job != "job_1" || report.ExitCode != 3 {
		t.Fatalf("unexpected report %+v", report)
	}
}

// A message before a command starts does not cut its wait, and a command that reaches its limit says nothing of a message.
func TestALaterCommandWaitsItsFullLimit(t *testing.T) {
	r := newRig("", syscall.SIGKILL)
	r.server.StepAside()
	answered := make(chan string)
	go func() {
		_, body := call(t, r.server.Handler(), "/run", `{"command":"make"}`)
		answered <- body
	}()
	timer := <-r.timers
	select {
	case body := <-answered:
		t.Fatalf("answered before its limit: %s", body)
	case <-time.After(50 * time.Millisecond):
	}
	timer.fire <- time.Time{}
	if body := <-answered; !strings.Contains(body, `"running":true`) || strings.Contains(body, "message_arrived") {
		t.Fatalf("run = %s", body)
	}
	r.proc.exit <- 0
	<-r.reports
}

func TestFastCommandReturnsItsExitWithoutReport(t *testing.T) {
	r := newRig("ok\n", syscall.SIGKILL)
	r.proc.exit <- 0
	j, err := r.server.run(nil, Run{Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if got := result(j); got.Output != "ok\n" || got.Status != "exit 0" || got.Running || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("unexpected result %+v", got)
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
		j, _ := r.server.run(nil, Run{Command: "sleep 99"})
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
		j, _ := r.server.run(nil, Run{Command: "sleep 99"})
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

func call(t *testing.T, handler http.Handler, path, body string) (int, string) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return response.Code, response.Body.String()
}

func TestHandlerRunsACommand(t *testing.T) {
	r := newRig("secret=abc\n", syscall.SIGKILL)
	r.proc.exit <- 0
	status, body := call(t, r.server.Handler(), "/run", `{"command":"env"}`)
	if status != http.StatusOK || !strings.Contains(body, "secret=[masked]") || strings.Contains(body, "abc") || !strings.Contains(body, `"status":"exit 0"`) || !strings.Contains(body, `"exit_code":0`) || !strings.Contains(body, `"job":"job_1"`) {
		t.Fatalf("run = %d %s", status, body)
	}
}

func TestHandlerRefusesWithATypedError(t *testing.T) {
	r := newRig("", syscall.SIGKILL)
	for path, input := range map[string]string{
		"/run": `{"command":" "}`,
		"/job": `{"action":"output","job":"job_9"}`,
	} {
		if status, body := call(t, r.server.Handler(), path, input); status != http.StatusBadRequest || !strings.Contains(body, `"code":"invalid"`) {
			t.Fatalf("%s = %d %s", path, status, body)
		}
	}
}

func TestBadCwdFailsClearly(t *testing.T) {
	server := New(Config{After: time.After})
	status, body := call(t, server.Handler(), "/run", "{\"command\":\"true\",\"cwd\":\"/box/no\u200bpe\"}")
	if status != http.StatusBadRequest || !strings.Contains(body, `cwd \"/box/no\\u200bpe\" does not exist`) {
		t.Fatalf("run = %d %s", status, body)
	}
}

func TestHandlerPassesCwdAndCallID(t *testing.T) {
	r := newRig("", syscall.SIGKILL)
	var dir, id string
	r.server.cfg.start = func(_, cwd, callID string, _ io.Writer) (process, error) {
		dir, id = cwd, callID
		return r.proc, nil
	}
	r.proc.exit <- 0
	call(t, r.server.Handler(), "/run", `{"command":"true","cwd":"/box/work/repo.worktrees/one","callId":"call-1"}`)
	if dir != "/box/work/repo.worktrees/one" || id != "call-1" {
		t.Fatalf("cwd %q, call ID %q", dir, id)
	}
}

func TestHandlerListsReadsAndStopsJobs(t *testing.T) {
	r := newRig("line\n", syscall.SIGTERM)
	finish := make(chan string, 1)
	go func() {
		_, body := call(t, r.server.Handler(), "/run", `{"command":"sleep 99"}`)
		finish <- body
	}()
	(<-r.timers).fire <- time.Now()
	if body := <-finish; !strings.Contains(body, `"running":true`) || !strings.Contains(body, `"status":"running"`) {
		t.Fatalf("run = %s", body)
	}
	if _, body := call(t, r.server.Handler(), "/job", `{"action":"list"}`); !strings.Contains(body, `"job":"job_1"`) || !strings.Contains(body, `"command":"sleep 99"`) {
		t.Fatalf("list = %s", body)
	}
	if _, body := call(t, r.server.Handler(), "/job", `{"action":"output","job":"job_1"}`); !strings.Contains(body, `"output":""`) {
		t.Fatalf("a call's own read leaves nothing unread: %s", body)
	}
	if _, body := call(t, r.server.Handler(), "/job", `{"action":"stop","job":"job_1"}`); !strings.Contains(body, `"status":"stopped"`) {
		t.Fatalf("stop = %s", body)
	}
	if _, body := call(t, r.server.Handler(), "/job", `{"action":"nothing"}`); !strings.Contains(body, "action is output") {
		t.Fatalf("unknown action = %s", body)
	}
}

// hostRig runs a command on a fake engine that answers when the test says.
func hostRig(answer func(context.Context, Run, io.Writer) (int, *contract.Error)) *rig {
	r := newRig("", syscall.SIGKILL)
	r.server.cfg.Host = answer
	return r
}

func TestHostCommandRunsThroughTheEngine(t *testing.T) {
	var asked Run
	r := hostRig(func(_ context.Context, run Run, out io.Writer) (int, *contract.Error) {
		asked = run
		_, _ = io.WriteString(out, "token=abc\n")
		return 3, nil
	})
	r.server.cfg.start = func(string, string, string, io.Writer) (process, error) {
		t.Fatal("a host command must not start in the box")
		return nil, nil
	}
	_, body := call(t, r.server.Handler(), "/run", `{"command":"uptime","host":"h","cwd":"logs","timeout":60}`)
	if asked.Host != "h" || asked.Command != "uptime" || asked.Cwd != "logs" || asked.Timeout != 60 {
		t.Fatalf("asked %+v", asked)
	}
	if !strings.Contains(body, `"exit_code":3`) || !strings.Contains(body, "token=[masked]") {
		t.Fatalf("run = %s", body)
	}
	if view := r.server.View(true); len(view) != 1 || view[0].Host != "h" || view[0].Command != "uptime" {
		t.Fatalf("view %+v", view)
	}
}

func TestRefusedHostCommandLeavesNoJob(t *testing.T) {
	r := hostRig(func(context.Context, Run, io.Writer) (int, *contract.Error) {
		return 0, &contract.Error{Code: contract.ErrNoGrant, Message: "no matching host-exec grant"}
	})
	status, body := call(t, r.server.Handler(), "/run", `{"command":"uptime","host":"h"}`)
	if status != http.StatusBadRequest || !strings.Contains(body, `"code":"no_grant"`) {
		t.Fatalf("run = %d %s", status, body)
	}
	if view := r.server.View(true); len(view) != 0 {
		t.Fatalf("view %+v", view)
	}
}

func TestSlowHostCommandMovesToBackgroundAndReportsItsEnd(t *testing.T) {
	release := make(chan struct{})
	r := hostRig(func(_ context.Context, _ Run, out io.Writer) (int, *contract.Error) {
		<-release
		_, _ = io.WriteString(out, "done\n")
		return 0, nil
	})
	finish := make(chan string, 1)
	go func() {
		_, body := call(t, r.server.Handler(), "/run", `{"command":"sleep 99","host":"h"}`)
		finish <- body
	}()
	(<-r.timers).fire <- time.Now()
	if body := <-finish; !strings.Contains(body, `"running":true`) {
		t.Fatalf("run = %s", body)
	}
	close(release)
	if got := <-r.reports; got.Job != "job_1" || got.ExitCode != 0 || !strings.Contains(got.Tail, "done") {
		t.Fatalf("report %+v", got)
	}
}

// job stop on a host command ends its call at once, which has the engine stop it on the host, and the job reads stopped.
func TestStoppingAHostCommandEndsItsCall(t *testing.T) {
	ended := make(chan struct{})
	r := hostRig(func(ctx context.Context, _ Run, _ io.Writer) (int, *contract.Error) {
		<-ctx.Done()
		close(ended)
		return 0, &contract.Error{Code: contract.ErrUnavailable, Message: ctx.Err().Error()}
	})
	finish := make(chan string, 1)
	go func() {
		_, body := call(t, r.server.Handler(), "/run", `{"command":"find / -name x","host":"h"}`)
		finish <- body
	}()
	(<-r.timers).fire <- time.Now()
	if body := <-finish; !strings.Contains(body, `"running":true`) {
		t.Fatalf("run = %s", body)
	}
	_, body := call(t, r.server.Handler(), "/job", `{"action":"stop","job":"job_1"}`)
	<-ended
	if !strings.Contains(body, `"status":"stopped"`) || strings.Contains(body, "unavailable") {
		t.Fatalf("stop = %s", body)
	}
	if _, body := call(t, r.server.Handler(), "/job", `{"action":"list"}`); !strings.Contains(body, `"status":"stopped"`) {
		t.Fatalf("list = %s", body)
	}
	// A stop the agent asked for wakes no one, as for a box command.
	select {
	case got := <-r.reports:
		t.Fatalf("report %+v", got)
	default:
	}
}

func TestLateHostRefusalReachesTheJobOutput(t *testing.T) {
	release := make(chan struct{})
	r := hostRig(func(context.Context, Run, io.Writer) (int, *contract.Error) {
		<-release
		return 0, &contract.Error{Code: contract.ErrNoGrant, Message: "no grant"}
	})
	finish := make(chan struct{})
	go func() {
		call(t, r.server.Handler(), "/run", `{"command":"x","host":"h"}`)
		close(finish)
	}()
	(<-r.timers).fire <- time.Now()
	<-finish
	close(release)
	if got := <-r.reports; got.ExitCode != 1 || !strings.Contains(got.Tail, "no_grant: no grant") {
		t.Fatalf("report %+v", got)
	}
}

func TestReportRetriesUntilTheEngineAccepts(t *testing.T) {
	r := newRig("", syscall.SIGKILL)
	r.failing = 1
	done := make(chan *job)
	go func() {
		j, _ := r.server.run(nil, Run{Command: "make"})
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
		if _, err := server.run(nil, Run{Command: "true"}); err != nil {
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
	j := &job{now: time.Now}
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
		j, _ := r.server.run(nil, Run{Command: "sleep 99 &"})
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
	if _, err := r.server.run(nil, Run{Command: "env"}); err != nil {
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
	j := &job{finished: true, now: time.Now}
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
	j := &job{now: time.Now}
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
	j := &job{now: time.Now, id: "j", command: "build " + token, done: make(chan struct{})}
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
	times := []time.Time{started, started, ended} // the second stamps the job's output
	r.server.cfg.now = func() time.Time { now := times[0]; times = times[1:]; return now }
	r.proc.exit <- 0
	j, err := r.server.run(nil, Run{Command: "make"})
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
		j := &job{now: time.Now, id: contract.ShellJobID(fmt.Sprint(i)), done: make(chan struct{})}
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
		s.jobs = append(s.jobs, &job{now: time.Now, id: contract.ShellJobID(fmt.Sprint(i)), command: strings.Repeat("é", 3000), done: make(chan struct{})})
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
			go func() { j, _ := r.server.run(nil, Run{Command: "build", Description: "Build the bundle"}); done <- j }()
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

//go:build linux

package shellmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"dyna-agent/internal/shells"
)

// TestMain also serves as the job supervisor: Tools spawns this test binary
// as `<exe> shell _run <id>`. The supervisor marks a job "noticed" when it
// would wake the owner.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "shell" && os.Args[2] == "_spawn" {
		if err := shells.StartSupervisor(os.Args[0], os.Args[3]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 4 && os.Args[1] == "shell" && os.Args[2] == "_run" {
		st := shells.DefaultStore()
		if err := st.Supervise(context.Background(), os.Args[3]); err != nil {
			os.Exit(1)
		}
		if unseen, err := st.ClaimNotice(os.Args[3]); err == nil && unseen {
			path, _ := st.OutputPath(os.Args[3])
			_ = os.WriteFile(filepath.Join(filepath.Dir(path), "noticed"), nil, 0600)
		}
		os.Exit(0)
	}
	for _, entry := range os.Environ() {
		if key, _, _ := strings.Cut(entry, "="); strings.HasPrefix(key, "DYNA_") {
			os.Unsetenv(key)
		}
	}
	os.Unsetenv("CODEX_THREAD_ID")
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	// Each job re-executes this binary twice, as launcher and supervisor. Keep
	// race checks without their one-second exit grace on every job. This
	// process already read GORACE, so only its children change.
	os.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	os.Exit(m.Run())
}

func newTools(t *testing.T, limit time.Duration) *Tools {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	return &Tools{Store: shells.DefaultStore(), Exe: os.Args[0], Owner: "test-owner", Cwd: t.TempDir(), Limit: limit}
}

func call(t *testing.T, tools *Tools, name string, args map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(args)
	var sent *Result
	deliver(context.Background(), tools, tools.Call(context.Background(), name, raw, ""), func(r Result) error { sent = &r; return nil })
	if sent == nil || sent.Failed {
		t.Fatalf("%s failed: %#v", name, sent)
	}
	return sent.Text
}

// fifo returns a path whose reader blocks until release is called.
func fifo(t *testing.T) (string, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gate")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	return path, func() {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

func onlyJob(t *testing.T, tools *Tools) shells.Info {
	t.Helper()
	list, err := tools.Store.List(true)
	if err != nil || len(list) != 1 {
		t.Fatalf("jobs = %#v, %v", list, err)
	}
	return list[0]
}

// supervised waits for the job's supervisor to exit and reports whether it
// would have woken the owner. The 30-second bound is only a safety limit; the
// wait ends on the exit event.
func supervised(t *testing.T, tools *Tools, id string) bool {
	t.Helper()
	in, err := tools.Store.Info(id)
	if err != nil {
		t.Fatal(err)
	}
	if fd, err := unix.PidfdOpen(in.Supervisor, 0); err == nil {
		defer unix.Close(fd)
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 30000)
		for err == unix.EINTR {
			n, err = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 30000)
		}
		if err != nil || n == 0 {
			t.Fatalf("supervisor %d did not exit: %v", in.Supervisor, err)
		}
	}
	path, _ := tools.Store.OutputPath(id)
	_, err = os.Stat(filepath.Join(filepath.Dir(path), "noticed"))
	return err == nil
}

func TestShellReturnsExitAndOutputOfAFastCommand(t *testing.T) {
	tools := newTools(t, 5*time.Second)
	text := call(t, tools, "shell", map[string]any{"command": "pwd; echo hi; exit 3", "description": "Say hi"})
	job := onlyJob(t, tools)
	if !strings.HasPrefix(text, "Job "+job.ID+". Exit code 3.\n") || !strings.Contains(text, tools.Cwd+"\nhi") {
		t.Fatalf("result = %q", text)
	}
	if job.Name != "Say hi" || job.Owner != "test-owner" || job.Cmd[0] != "bash" {
		t.Fatalf("job = %#v", job)
	}
	if supervised(t, tools, job.ID) {
		t.Fatal("the supervisor woke the owner for an exit the call already returned")
	}
	if m, err := tools.Store.ReadMeta(job.ID); err != nil || m.BackgroundAt != nil {
		t.Fatalf("a foreground job has backgroundAt = %v, %v", m.BackgroundAt, err)
	}
}

func TestShellMovesToBackgroundAndOutputWaitsForExit(t *testing.T) {
	tools := newTools(t, 50*time.Millisecond)
	gate, release := fifo(t)
	text := call(t, tools, "shell", map[string]any{"command": "echo started; cat " + gate + "; echo finished", "description": "Wait at the gate"})
	job := onlyJob(t, tools)
	if !strings.HasPrefix(text, "Job "+job.ID+". Still running after ") || !strings.Contains(text, "moved to the background.") {
		t.Fatalf("result = %q", text)
	}
	if m, err := tools.Store.ReadMeta(job.ID); err != nil || m.BackgroundAt == nil {
		t.Fatalf("backgroundAt = %v, %v", m.BackgroundAt, err)
	}
	release()
	later := call(t, tools, "shell_output", map[string]any{"job_id": job.ID, "wait": "exit", "timeout_seconds": 5})
	if !strings.HasPrefix(later, "Job "+job.ID+". Exit code 0.\n") {
		t.Fatalf("output = %q", later)
	}
	// Each line arrives once: output already returned is not repeated.
	if all := text + later; strings.Count(all, "started") != 1 || strings.Count(all, "finished") != 1 {
		t.Fatalf("outputs = %q then %q", text, later)
	}
	// The job may end before shell_output holds its exit. Then the owner is
	// also woken, which is right, so this test does not check the notice.
	// The visible exit precedes the supervisor's final writes. Join it before
	// TempDir removes the job folder and database.
	supervised(t, tools, job.ID)
}

func TestBackgroundJobWakesTheOwnerWhenNobodyWaits(t *testing.T) {
	tools := newTools(t, 50*time.Millisecond)
	gate, release := fifo(t)
	call(t, tools, "shell", map[string]any{"command": "cat " + gate, "description": "Wait at the gate"})
	job := onlyJob(t, tools)
	release()
	if !supervised(t, tools, job.ID) {
		t.Fatal("the supervisor did not wake the owner")
	}
}

func TestOutputWaitsForAPatternAndStopKillsTheGroup(t *testing.T) {
	tools := newTools(t, 50*time.Millisecond)
	first, releaseFirst := fifo(t)
	second, _ := fifo(t)
	call(t, tools, "shell", map[string]any{"command": "cat " + first + "; echo listening on 8080; cat " + second, "description": "Start a server"})
	job := onlyJob(t, tools)
	releaseFirst()
	text := call(t, tools, "shell_output", map[string]any{"job_id": job.ID, "wait": `listening on \d+`, "timeout_seconds": 5})
	if !strings.Contains(text, "is running. Its output matched") || !strings.Contains(text, "listening on 8080") {
		t.Fatalf("output = %q", text)
	}
	text = call(t, tools, "shell_stop", map[string]any{"job_id": job.ID})
	if !strings.HasPrefix(text, "Job "+job.ID+". Exit code -1 (terminated).") {
		t.Fatalf("stop = %q", text)
	}
	in, err := tools.Store.Info(job.ID)
	if err != nil || in.Status != "exited" || syscall.Kill(-in.Pid, 0) == nil {
		t.Fatalf("job after stop = %#v, %v", in, err)
	}
	if supervised(t, tools, job.ID) {
		t.Fatal("the supervisor woke the owner for a job the owner stopped")
	}
	if m, err := tools.Store.ReadMeta(job.ID); err != nil || m.StopBy != "agent" {
		t.Fatalf("stopBy = %q, %v", m.StopBy, err)
	}
}

func TestUndeliveredAnswerLeavesExitAndOutputUnseen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
		send   error
	}{
		{name: "cancelled request", cancel: true},
		{name: "failed write", send: io.ErrClosedPipe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := newTools(t, 5*time.Second)
			// The job has ended and its answer is ready, but not yet sent.
			r := tools.Call(context.Background(), "shell", json.RawMessage(`{"command":"echo once","description":"Echo once"}`), "")
			if r.Failed || !strings.HasPrefix(r.Text, "Job "+onlyJob(t, tools).ID+". Exit code 0.") {
				t.Fatalf("result = %#v", r)
			}
			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancel {
				cancel()
			}
			defer cancel()
			sent := false
			deliver(ctx, tools, r, func(Result) error { sent = true; return tc.send })
			if tc.cancel && sent {
				t.Fatal("a cancelled request got a response")
			}
			job := onlyJob(t, tools)
			if !supervised(t, tools, job.ID) {
				t.Fatal("an undelivered exit did not wake the owner")
			}
			if text := call(t, tools, "shell_output", map[string]any{"job_id": job.ID}); text != "Job "+job.ID+". Exit code 0.\nonce\n" {
				t.Fatalf("unseen output was skipped: %q", text)
			}
		})
	}
}

func TestJobsBelongToTheirSession(t *testing.T) {
	tools := newTools(t, 5*time.Second)
	call(t, tools, "shell", map[string]any{"command": "true", "description": "Do nothing"})
	job := onlyJob(t, tools)
	other := &Tools{Store: tools.Store, Exe: tools.Exe, Owner: "other-owner", Cwd: tools.Cwd}
	if r := other.Call(context.Background(), "shell_stop", json.RawMessage(`{"job_id":"`+job.ID+`"}`), ""); !r.Failed || !strings.Contains(r.Text, "another session") {
		t.Fatalf("foreign stop = %#v", r)
	}
	if text := call(t, other, "shell_jobs", nil); text != "No jobs." {
		t.Fatalf("foreign jobs = %q", text)
	}
	if text := call(t, tools, "shell_jobs", nil); !strings.Contains(text, job.ID+"  exited with code 0") || !strings.HasSuffix(text, "Do nothing\n") {
		t.Fatalf("jobs = %q", text)
	}
	if r := tools.Call(context.Background(), "shell", json.RawMessage(`{"command":"true"}`), ""); !r.Failed || !strings.Contains(r.Text, "description is required") {
		t.Fatalf("missing description = %#v", r)
	}
}

func TestForegroundRunsPastTheLimitWithoutBackgrounding(t *testing.T) {
	tools := newTools(t, 50*time.Millisecond)
	tools.Foreground = true
	gate, release := fifo(t)
	done := make(chan string, 1)
	go func() {
		raw, _ := json.Marshal(map[string]any{"command": "cat " + gate + "; echo through", "description": "Wait at the gate"})
		r := tools.Call(context.Background(), "shell", raw, "")
		done <- r.Text
	}()
	release()
	if text := <-done; text != "Job "+onlyJob(t, tools).ID+". Exit code 0.\nthrough\n" {
		t.Fatalf("foreground result = %q", text)
	}
	if strings.Contains(tools.tools()[0]["description"].(string), "background") {
		t.Fatal("the foreground shell tool still promises backgrounding")
	}
}

// If the server dies after a job ends but before its answer is delivered,
// the exit must not stay held for the whole wait limit (24 h in foreground
// mode): the supervisor wakes the session soon after.
func TestUndeliveredExitIsReleasedSoonAfterTheJobEnds(t *testing.T) {
	grace := holdGrace
	holdGrace = 50 * time.Millisecond
	t.Cleanup(func() { holdGrace = grace })
	tools := newTools(t, time.Second)
	tools.Foreground = true
	// Neither Commit nor Abort runs, as if the server died after Call.
	if r := tools.Call(context.Background(), "shell", json.RawMessage(`{"command":"echo once","description":"Echo once"}`), ""); r.Failed {
		t.Fatalf("result = %#v", r)
	}
	if !supervised(t, tools, onlyJob(t, tools).ID) {
		t.Fatal("the held exit was never released to the supervisor")
	}
}

func TestForegroundCloseStopsJobsStillRunning(t *testing.T) {
	tools := newTools(t, time.Second)
	tools.Foreground = true
	gate, _ := fifo(t)
	readyPath, _ := fifo(t)
	readyFile, err := os.OpenFile(readyPath, os.O_RDWR|syscall.O_NONBLOCK, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer readyFile.Close()
	ready := make(chan error, 1)
	go func() {
		_, err := readyFile.Read(make([]byte, 1))
		ready <- err
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	readyWait, stopWaiting := context.WithTimeout(ctx, 30*time.Second)
	defer stopWaiting()
	started := make(chan Result, 1)
	go func() {
		raw, _ := json.Marshal(map[string]any{"command": "echo ready > " + readyPath + "; cat " + gate, "description": "Wait forever"})
		started <- tools.Call(ctx, "shell", raw, "")
	}()
	// The command signals that it started before it waits at the gate.
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal("command-ready event:", err)
		}
	case r := <-started:
		t.Fatalf("command returned before signaling ready: %+v", r)
	case <-readyWait.Done():
		t.Fatal("waiting for command-ready event:", readyWait.Err())
	}
	// The client goes away while the command still runs.
	job := onlyJob(t, tools)
	cancel()
	if r := <-started; !r.Failed {
		t.Fatalf("cancelled call = %#v", r)
	}
	if err := tools.Close(); err != nil {
		t.Fatal(err)
	}
	if in, err := tools.Store.Info(job.ID); err != nil || in.Status != "exited" {
		t.Fatalf("job after Close = %#v, %v", in, err)
	}
	// The command exit precedes the supervisor's final writes and notice.
	// Join that process before TempDir removes its database and job folder.
	supervised(t, tools, job.ID)
}

func TestServeSpeaksMCP(t *testing.T) {
	tools := newTools(t, 5*time.Second)
	in, writer := io.Pipe()
	reader, out := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), in, out, tools); out.Close() }()
	responses := bufio.NewScanner(reader)
	exchange := func(line string) map[string]any {
		t.Helper()
		if _, err := io.WriteString(writer, line+"\n"); err != nil {
			t.Fatal(err)
		}
		if !responses.Scan() {
			t.Fatalf("no response to %s", line)
		}
		var got map[string]any
		if err := json.Unmarshal(responses.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	init := exchange(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if result := init["result"].(map[string]any); result["protocolVersion"] != "2025-03-26" || result["serverInfo"].(map[string]any)["name"] != "dyna" {
		t.Fatalf("initialize = %v", init)
	}
	if _, err := io.WriteString(writer, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	list := exchange(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var names []string
	for _, tool := range list["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "shell,shell_output,shell_stop,shell_jobs" {
		t.Fatalf("tools = %v", names)
	}
	result := exchange(`{"jsonrpc":"2.0","id":"three","method":"tools/call","params":{"name":"shell","arguments":{"command":"echo mcp","description":"Echo"}}}`)
	content := result["result"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if result["id"] != "three" || !strings.HasSuffix(content["text"].(string), ". Exit code 0.\nmcp\n") || !strings.HasPrefix(content["text"].(string), "Job sh_") || result["result"].(map[string]any)["isError"] != false {
		t.Fatalf("call = %v", result)
	}
	if missing := exchange(`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`); missing["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatalf("unknown method = %v", missing)
	}
	writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The response can precede the supervisor's final writes.
	// Join it before TempDir removes its job folder and database.
	supervised(t, tools, onlyJob(t, tools).ID)
}

func TestBoundedOutputKeepsHeadAndTail(t *testing.T) {
	data := strings.Repeat("a", outputHead) + strings.Repeat("m", 30<<10) + strings.Repeat("z", maxOutput-outputHead)
	got, err := bounded(strings.NewReader(data), 0, int64(len(data)), "/log")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("a", outputHead) + "\n[... 30720 bytes cut; full log: /log ...]\n" + strings.Repeat("z", maxOutput-outputHead)
	if got != want {
		t.Fatalf("bounded output has %d bytes, want %d", len(got), len(want))
	}
	if got, _ := bounded(strings.NewReader("abc"), 1, 3, "/log"); got != "bc" {
		t.Fatalf("small output = %q", got)
	}
	if got, _ := bounded(strings.NewReader(""), 0, 0, "/log"); got != "(no new output)" {
		t.Fatalf("empty output = %q", got)
	}
}

func TestWaitCapMovesLongCommandsToTheBackground(t *testing.T) {
	tools := newTools(t, 0)
	tools.WaitCap = 200 * time.Millisecond
	gate, release := fifo(t)
	text := call(t, tools, "shell", map[string]any{"command": "cat " + gate, "description": "Wait for the gate"})
	if !strings.Contains(text, "Still running after 200ms") {
		t.Fatalf("a command outlived the cap: %s", text)
	}
	job := onlyJob(t, tools)
	if got := call(t, tools, "shell_output", map[string]any{"job_id": job.ID, "wait": "exit", "timeout_seconds": 600}); !strings.Contains(got, "still running after 200ms") {
		t.Fatalf("shell_output outlived the cap: %s", got)
	}
	if desc := tools.tools()[1]["description"].(string); !strings.Contains(desc, "at most 200ms") {
		t.Fatalf("shell_output description does not say the cap: %s", desc)
	}
	release()
	tools.WaitCap = 0
	call(t, tools, "shell_output", map[string]any{"job_id": job.ID, "wait": "exit"})
}

func TestCallRunsForTheSessionTheClientNames(t *testing.T) {
	tools := newTools(t, time.Minute)
	work := t.TempDir()
	tools.Resolve = func(id string) (Caller, bool) {
		if id != "oc-session" {
			return Caller{}, false
		}
		return Caller{Owner: "mapped-owner", RoomID: "room-1", Room: "work", Cwd: work, Env: []string{"DYNA_SESSION=mapped-owner"}}, true
	}
	run := func(clientSession string) string {
		raw, _ := json.Marshal(map[string]any{"command": "echo $DYNA_SESSION:$PWD", "description": "Show the identity"})
		var sent *Result
		deliver(context.Background(), tools, tools.Call(context.Background(), "shell", raw, clientSession), func(r Result) error { sent = &r; return nil })
		if sent == nil || sent.Failed {
			t.Fatalf("shell failed: %#v", sent)
		}
		return sent.Text
	}
	if got := run("oc-session"); !strings.Contains(got, "mapped-owner:"+work) {
		t.Errorf("mapped call = %q, want session mapped-owner in %s", got, work)
	}
	if got := run("someone-else"); !strings.Contains(got, ":"+tools.Cwd) || strings.Contains(got, "mapped-owner") {
		t.Errorf("unmapped call = %q, want no mapped session in %s", got, tools.Cwd)
	}
	list, err := tools.Store.List(true)
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{}
	for _, in := range list {
		owners[in.Owner] = in.RoomID
	}
	if owners["mapped-owner"] != "room-1" || owners["test-owner"] != "" || len(owners) != 2 {
		t.Errorf("job owners = %v", owners)
	}
}

func TestOpenCodeClientSetsTheCapAndNamesItsSession(t *testing.T) {
	tools := newTools(t, time.Minute)
	var seen string
	tools.Resolve = func(id string) (Caller, bool) { seen = id; return Caller{}, false }
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"opencode","version":"2.0.32"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"shell_jobs","arguments":{},"_meta":{"ai.opencode/sessionID":"ses_oc_1"}}}` + "\n")
	var out strings.Builder
	if err := Serve(context.Background(), in, &out, tools); err != nil {
		t.Fatal(err)
	}
	if tools.WaitCap != openCodeWaitCap || seen != "ses_oc_1" {
		t.Errorf("cap = %v, client session = %q", tools.WaitCap, seen)
	}
}

func TestLostJobReportsSupervisorErrors(t *testing.T) {
	tools := newTools(t, time.Second)
	meta, err := tools.Store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), Owner: tools.Owner})
	if err != nil {
		t.Fatal(err)
	}
	// The supervisor never recorded itself and has been gone for a minute.
	meta.Started = time.Now().Add(-time.Minute)
	if err := tools.Store.Save(meta, nil); err != nil {
		t.Fatal(err)
	}
	path, _ := tools.Store.OutputPath(meta.ID)
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "supervisor.log"), []byte("Error: database is locked (5) (SQLITE_BUSY)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	in, err := tools.Store.Info(meta.ID)
	if err != nil || in.Status != "lost" {
		t.Fatalf("job = %#v, %v", in, err)
	}
	if line := tools.exitLine(in); !strings.Contains(line, "was lost") || !strings.Contains(line, "SQLITE_BUSY") {
		t.Fatalf("lost report = %q", line)
	}
}

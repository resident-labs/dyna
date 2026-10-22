package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callerid"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engine"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/harness/fakeruntime"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

var (
	binOnce sync.Once
	binPath string
	binErr  error
	binDir  string
	// schemaImage is immutable. Each fake engine opens its own file copy.
	schemaImage []byte
)

func TestMain(m *testing.M) {
	// A test run must not inherit the invoking Dyna session's settings.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DYNA_") {
			os.Unsetenv(key)
		}
	}
	os.Setenv("DYNA_COMPUTERS_ENABLED", "0")
	os.Setenv("DYNA_SLICE", "off")
	// Build the shared CLI and prepare an empty schema before tests start.
	// Each fixture later owns a private writable copy of that schema.
	code := 1
	if runtime.GOOS != "windows" {
		started := time.Now()
		prepareDynaBinary()
		fmt.Fprintf(os.Stderr, "CLI fixture build completed in %.3f seconds.\n", time.Since(started).Seconds())
	}
	var schemaErr error
	if runtime.GOOS != "windows" && binErr == nil {
		started := time.Now()
		schemaErr = prepareE2ESchema()
		fmt.Fprintf(os.Stderr, "CLI fixture schema prepared in %.3f seconds.\n", time.Since(started).Seconds())
	}
	if binErr != nil {
		fmt.Fprintf(os.Stderr, "build CLI fixture: %v\n", binErr)
	} else if schemaErr != nil {
		fmt.Fprintf(os.Stderr, "prepare CLI fixture schema: %v\n", schemaErr)
	} else {
		code = m.Run()
	}
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(code)
}

// prepareE2ESchema runs the real engine migrations once for the package.
// Close checkpoints the WAL before reading the file; no fixture shares a
// live connection or depends on the template directory after this returns.
func prepareE2ESchema() error {
	dir, err := os.MkdirTemp("", "dyna-e2e-schema-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := engine.MigrateStores(dir); err != nil {
		_ = database.Close(dir)
		return err
	}
	if err := database.Close(dir); err != nil {
		return err
	}
	schemaImage, err = os.ReadFile(filepath.Join(dir, "dyna.db"))
	return err
}

// prepareDynaBinary builds the shared command used by CLI tests.
func prepareDynaBinary() {
	binOnce.Do(func() {
		binDir, binErr = os.MkdirTemp("", "dyna-e2e-bin-")
		if binErr != nil {
			return
		}
		binPath = filepath.Join(binDir, "dyna")
		root, _ := os.Getwd()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", binPath, ".")
		build.Dir = root
		if out, err := build.CombinedOutput(); err != nil {
			binErr = &buildError{err: err, out: string(out)}
		}
	})
}

func dynaBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("subagent runners use Unix flock and Setsid")
	}
	prepareDynaBinary()
	if binErr != nil {
		t.Fatalf("build dyna: %v", binErr)
	}
	return binPath
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.out }

// e2e is an isolated dyna installation with its own data and config directories.
// It never touches the live store.
type e2e struct {
	t      *testing.T
	bin    string
	data   string
	config string
	extra  []string // KEY=value overrides
	events *hub.Hub
	// dir is the git repo commands run in; room is that repo's room "work".
	dir  string
	room room.Record
}

func e2eData(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "dyna")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "dyna.db"), schemaImage, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func newE2E(t *testing.T, profiles ...profile.Profile) *e2e {
	t.Helper()
	e := &e2e{t: t, bin: dynaBinary(t), data: e2eData(t), config: t.TempDir()}
	e.serveEngine()
	store := &profile.Store{Path: filepath.Join(e.config, "dyna", "profiles.json"), Profiles: profiles}
	if len(profiles) == 0 {
		store.Profiles = []profile.Profile{
			{Name: "mock", Description: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Default: true},
		}
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	e.dir = filepath.Join(t.TempDir(), "work")
	if out, err := exec.Command("git", "init", "-q", e.dir).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	repo, err := room.RepoRoot(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if e.room, err = (&room.Store{Root: filepath.Join(e.data, "dyna", "rooms")}).Create(room.CreateInput{Name: "work", Repo: repo}); err != nil {
		t.Fatal(err)
	}
	// Join while the fake engine can still take a runner's final write.
	// Registered after serveEngine, so cleanup runs it before the socket closes.
	t.Cleanup(e.joinRunners)
	return e
}

// waitE2ERunner uses ps on Unix hosts. Linux tests replace it with pidfd polling.
var waitE2ERunner = func(ctx context.Context, pid int, identity string) error {
	for {
		cmd := exec.CommandContext(ctx, "ps", "-ww", "-p", strconv.Itoa(pid), "-o", "stat=", "-o", "args=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 && len(bytes.TrimSpace(out)) == 0 {
				return nil
			}
			return err
		}
		state := strings.TrimSpace(string(out))
		if strings.HasPrefix(state, "Z") || !strings.Contains(state, identity) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (e *e2e) joinRunners() {
	e.t.Helper()
	// Manager.Stop is the production stop, called as the owner. It records
	// StopAsked for a queued runner and stops a live one. The fixture must
	// not write a stop-<turn> file of its own.
	m := &subagent.Manager{
		Store:      &subagent.Store{Root: filepath.Join(e.data, "dyna", "sessions")},
		Executable: e.bin,
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		owner := authority.WithActor(ctx, authority.Actor{Role: authority.Owner})
		err := e.stopFixtureSessions(owner, m)
		cancel()
		if err != nil {
			e.t.Errorf("stop fixture sessions: %v", err)
			return
		}
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		runners, err := e.fixtureRunners(ctx)
		cancel()
		if err != nil {
			e.t.Errorf("list fixture runners: %v", err)
			return
		}
		if len(runners) == 0 {
			return
		}
		for _, r := range runners {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := waitE2ERunner(ctx, r.pid, r.identity)
			cancel()
			if err != nil {
				e.t.Errorf("join fixture runner (PID %d): %v", r.pid, err)
				return
			}
		}
		// Final runner work can start another turn. Stop and join that too.
	}
}

// stopFixtureSessions asks every running or queued fixture session to stop.
func (e *e2e) stopFixtureSessions(ctx context.Context, m *subagent.Manager) error {
	infos, err := m.Store.ListSnapshot(false)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, info := range infos {
		if info.Status != subagent.StatusRunning && info.Status != subagent.StatusQueued {
			continue
		}
		if _, err := m.Stop(ctx, info.ID); err != nil {
			if _, idle := err.(*subagent.NoRunningTurnError); idle {
				continue
			}
			return err
		}
	}
	return nil
}

// e2eRunner is one live turn process for this fixture.
type e2eRunner struct {
	pid      int
	identity string
}

// fixtureRunners is the live turn processes for this fixture's sessions.
func (e *e2e) fixtureRunners(ctx context.Context) ([]e2eRunner, error) {
	out, err := exec.CommandContext(ctx, "ps", "-eww", "-o", "pid=", "-o", "args=").Output()
	if err != nil {
		return nil, err
	}
	var runners []e2eRunner
	for _, line := range strings.Split(string(out), "\n") {
		pidText, args, ok := strings.Cut(strings.TrimSpace(line), " ")
		pid, err := strconv.Atoi(pidText)
		if !ok || err != nil {
			continue
		}
		tail, ok := strings.CutPrefix(strings.TrimSpace(args), e.bin+" subagent _turn ")
		fields := strings.Fields(tail)
		if !ok || len(fields) != 2 {
			continue
		}
		dir := filepath.Join(e.data, "dyna", "sessions", fields[0])
		if _, err := os.Stat(dir); err != nil {
			continue // This runner belongs to another fixture.
		}
		runners = append(runners, e2eRunner{pid, "subagent _turn " + strings.Join(fields, " ")})
	}
	return runners, nil
}

func TestE2ECleanupStopsActiveRunner(t *testing.T) {
	e := newE2E(t)
	started := e.startSession("--profile", "mock", "--name", "cleanup", "RESPOND: done SLEEP:30000")
	e.joinRunners()
	if status := e.show(started.Address).Session.Status; status == "running" || status == "queued" {
		out, _ := exec.Command("ps", "-eww", "-o", "pid=", "-o", "args=").Output()
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, e.bin+" subagent _turn "+started.Session) {
				t.Logf("runner process: %s", line)
			}
		}
		t.Fatalf("fixture cleanup left an active runner: %s", status)
	}
}

func (e *e2e) env() []string {
	env := withoutDynaEnv(os.Environ())
	// Reapply suite defaults after removing inherited settings.
	env = setTestEnv(env, "DYNA_COMPUTERS_ENABLED", "0")
	env = setTestEnv(env, "DYNA_SLICE", "off")
	env = setTestEnv(env, "XDG_DATA_HOME", e.data)
	env = setTestEnv(env, "XDG_CONFIG_HOME", e.config)
	env = setTestEnv(env, "DYNA_NO_AUTO_UPDATE", "1")
	for _, kv := range e.extra {
		key, value, _ := strings.Cut(kv, "=")
		env = setTestEnv(env, key, value)
	}
	return env
}

func (e *e2e) run(args ...string) (stdout, stderr string, err error) {
	e.t.Helper()
	return e.runEnv(nil, args...)
}

// serveEngine stands in for the engine's Post and CreateRoomRecord calls on
// a temporary socket: dyna post and dyna room create send them to the
// engine. Post runs the posting path the engine runs for a verified caller.
// Like the rest of this fixture, it leaves delivery to deliverSessionPosts.
// It also serves the turn runners' writes, for the caller a runner's launch
// record names, as the engine does.
func (e *e2e) serveEngine() {
	e.t.Helper()
	e.t.Setenv("XDG_DATA_HOME", e.data)
	e.t.Setenv("XDG_CONFIG_HOME", e.config)
	calls, err := websvc.Bind([]websvc.Binding{{Name: "Post", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(ctx context.Context, req notify.PostRequest) (notify.Result, error) {
			svc := notify.Default()
			svc.Sessions.Executable = e.bin
			svc.EngineRunning = func() bool { return false }
			claim := notify.Claim{Room: req.Claim.Room}
			if caller, _ := websvc.CallerFromContext(ctx); caller.Kind == websvc.Session {
				claim.Session = caller.Session
			}
			snd, err := svc.SenderOf(claim)
			if err != nil {
				return notify.Result{}, err
			}
			result, err := svc.Post(ctx, req.Input(snd))
			// Production publishes appended posts through the room watch.
			// This fixture has no watch, so announce the stored post here.
			if result.Message.ID != "" {
				e.events.Publish(hub.Event{Topic: "posts", Room: result.Message.RoomID})
			}
			return result, err
		}},
		{Name: "CreateRoomRecord", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: roomsvc.New(nil).CreateRoomRecord},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	// The session controls that the CLI calls, as the engine serves them.
	svc := notify.Default()
	svc.Sessions.Executable = e.bin
	e.events = hub.New(1)
	svc.Sessions.InputEvents = e.events
	db, err := database.Open(filepath.Join(e.data, "dyna"))
	if err != nil {
		e.t.Fatal(err)
	}
	state.For(db).SetPublish(e.events.Publish)
	e.t.Cleanup(func() { state.For(db).SetPublish(nil) })
	rooms := roomsvc.New(svc)
	sessions, err := engine.SessionCalls(rooms)
	if err != nil {
		e.t.Fatal(err)
	}
	for name, call := range sessions {
		calls[name] = call
	}
	// Runner completion uses the same configured notify service as session
	// controls. Store the report before acknowledging the fixture call.
	svc.Sessions.TurnReporter = func(ctx context.Context, _ *subagent.Manager, id string, turn int) error {
		_, err := svc.CompleteSessionTurn(ctx, id, turn)
		return err
	}
	bindings := append(engine.RunnerBindings(func() *subagent.Manager {
		return svc.Sessions
	}), engine.NativeAdmissionBinding(rooms, harnessruntime.New()))
	runners, err := websvc.Bind(bindings)
	if err != nil {
		e.t.Fatal(err)
	}
	for name, call := range runners {
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			caller, _ := websvc.CallerFromContext(ctx)
			return call(authority.WithActor(ctx, authority.Actor{Role: authority.Session, Session: caller.Session}), args)
		}
	}
	// All fixture calls act for their recorded caller, including owner CLIs.
	for name, call := range calls {
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			actor := authority.Actor{Role: authority.Owner}
			if caller, _ := websvc.CallerFromContext(ctx); caller.Kind == websvc.Session {
				var err error
				actor, err = svc.Sessions.Store.SessionActor(caller.Session)
				if err != nil {
					return nil, err
				}
			}
			return call(authority.WithActor(ctx, actor), args)
		}
	}
	dataDir := filepath.Join(e.data, "dyna")
	enginetest.ServeCallers(e.t, dataDir, e.runnerCallers(dataDir), calls)
}

// e2eCallerRecords makes in-process CLI assertions the owner of their
// private fake engine. Other processes keep their recorded launch identity.
type e2eCallerRecords struct {
	callerid.Store
	owner callerid.Process
}

func (r e2eCallerRecords) ProcessIdentity(p callerid.Process) (callerid.Identity, bool, error) {
	if p == r.owner {
		return callerid.Identity{Kind: callerid.Owner}, true, nil
	}
	return r.Store.ProcessIdentity(p)
}

// runnerCallers uses launch records for both fixture CLI commands and real
// turn runners. It never grants authority from an unverified claim.
func (e *e2e) runnerCallers(dataDir string) func(net.Conn) (websvc.Caller, error) {
	e.t.Helper()
	records, err := callerid.OpenStore(dataDir)
	if err != nil {
		e.t.Fatal(err)
	}
	owner, err := callerid.ProcessOf(os.Getpid())
	if err != nil {
		e.t.Fatal(err)
	}
	r, err := callerid.New("/proc", 0, e2eCallerRecords{Store: records, owner: owner})
	if err != nil {
		e.t.Fatal(err)
	}
	return func(c net.Conn) (websvc.Caller, error) {
		id := r.ResolveConn(c)
		switch id.Kind {
		case callerid.Session:
			return websvc.Caller{Kind: websvc.Session, Session: id.Session}, nil
		case callerid.Owner:
			return websvc.Caller{Kind: websvc.Owner}, nil
		default:
			return websvc.Caller{}, fmt.Errorf("fixture caller refused: %s", id.Reason)
		}
	}
}

// deliverSessionPosts drives one engine worker pass against the fixture store.
// It starts only the fixture CLI and mock or installed fake harnesses, without
// starting an engine server or changing the live data directories.
func (e *e2e) deliverSessionPosts() {
	e.t.Helper()
	e.t.Setenv("XDG_DATA_HOME", e.data)
	e.t.Setenv("XDG_CONFIG_HOME", e.config)
	e.t.Setenv("DYNA_NO_AUTO_UPDATE", "1")
	m := subagent.NewManager()
	m.Executable = e.bin
	ctx, cancel := context.WithTimeout(e.t.Context(), 30*time.Second)
	defer cancel()
	if _, err := m.TickSessions(ctx); err != nil {
		e.t.Fatalf("fixture session delivery: %v", err)
	}
}

func (e *e2e) runEnv(extra []string, args ...string) (string, string, error) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Dir = e.dir
	cmd.Env = e.env()
	for _, kv := range extra {
		key, value, _ := strings.Cut(kv, "=")
		cmd.Env = setTestEnv(cmd.Env, key, value)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := e.startCLI(ctx, cmd)
	if err == nil {
		err = cmd.Wait()
	}
	return out.String(), errb.String(), err
}

// startCLI records the test's chosen identity before its command can call
// the fake engine. Environment stamps select a fixture, never production authority.
func (e *e2e) startCLI(ctx context.Context, cmd *exec.Cmd) error {
	dataDir := filepath.Join(e.data, "dyna")
	for _, entry := range cmd.Env {
		if id, ok := strings.CutPrefix(entry, "DYNA_SESSION="); ok && id != "" {
			return callerid.Start(ctx, cmd, callerid.Launch{DataDir: dataDir, Name: id,
				Actor: callerid.Identity{Kind: callerid.Session, Session: id}, Launcher: "fixture-cli"})
		}
	}
	restore := authority.InEngine()
	defer restore()
	return callerid.StartTerminal(ctx, cmd, dataDir, cmd.Start)
}

func (e *e2e) must(args ...string) string {
	e.t.Helper()
	out, stderr, err := e.run(args...)
	if err != nil {
		e.t.Fatalf("dyna %s: %v\nstdout=%s\nstderr=%s", strings.Join(args, " "), err, out, stderr)
	}
	return out
}

// startResult is `dyna subagent start --json`.
type startResult struct {
	Session string `json:"session"`
	Address string `json:"address"`
	Name    string `json:"name"`
	Room    string `json:"room"`
	RoomID  string `json:"roomId"`
	Profile string `json:"profile"`
	Turn    int    `json:"turn"`
}

// startSession starts a session with --json and returns its envelope. The
// task text must be the last argument, as on the real command line.
func (e *e2e) startSession(args ...string) startResult {
	e.t.Helper()
	full := append([]string{"subagent", "start", "--goal", "Finish the test task", "--json"}, args...)
	var res startResult
	if err := json.Unmarshal([]byte(e.must(full...)), &res); err != nil {
		e.t.Fatalf("decode start --json: %v", err)
	}
	if !strings.HasPrefix(res.Session, "ses_") || !strings.Contains(res.Address, ":") {
		e.t.Fatalf("start --json = %#v", res)
	}
	return res
}

// showJSON is `dyna subagent show --json`. Tests poll it to observe turns:
// the product has no wait command, and polling the store through show is
// the harness-side equivalent.
type showJSON struct {
	Session struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		Room           string `json:"room"`
		Status         string `json:"status"`
		HarnessSession string `json:"harnessSession"`
		Turns          int    `json:"turns"`
		Depth          int    `json:"depth"`
		ParentSession  string `json:"parentSession"`
	} `json:"session"`
	Turns []struct {
		N              int             `json:"n"`
		Status         string          `json:"status"`
		Result         json.RawMessage `json:"result"`
		Error          string          `json:"error"`
		Reported       bool            `json:"reported"`
		HarnessSession string          `json:"harnessSession"`
		Steering       []struct {
			ID        string `json:"id"`
			Message   string `json:"message"`
			Delivered bool   `json:"delivered"`
		} `json:"steering"`
	} `json:"turns"`
}

func (s showJSON) turn(n int) (status, result string, ok bool) {
	for _, t := range s.Turns {
		if t.N != n {
			continue
		}
		if len(t.Result) > 0 && string(t.Result) != "null" {
			var text string
			if err := json.Unmarshal(t.Result, &text); err == nil {
				result = text
			} else {
				result = string(t.Result)
			}
		}
		return t.Status, result, true
	}
	return "", "", false
}

func (e *e2e) show(ref string) showJSON {
	e.t.Helper()
	var s showJSON
	if err := json.Unmarshal([]byte(e.must("subagent", "show", ref, "--json")), &s); err != nil {
		e.t.Fatalf("decode show --json for %s: %v", ref, err)
	}
	return s
}

// waitTurn polls show until turn n reaches a terminal status, then returns
// its status and result text (string results unquoted, JSON results raw).
// waitReported waits until turn n of ref has made its report decision (a
// report was sent, or the turn ended silently).
func (e *e2e) waitReported(ref string, n int) {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, turn := range e.show(ref).Turns {
			if turn.N == n && turn.Reported {
				return
			}
		}
		time.Sleep(e2ePoll)
	}
	e.t.Fatalf("%s turn %d never made its report decision", ref, n)
}

// e2ePoll spaces status polls; each poll is a short CLI process, so a small
// interval keeps waits close to the event they wait for.
const e2ePoll = 25 * time.Millisecond

func (e *e2e) waitTurn(ref string, n int) (status, result string) {
	e.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last showJSON
	for {
		last = e.show(ref)
		if st, res, ok := last.turn(n); ok {
			switch st {
			case "error":
				for _, turn := range last.Turns {
					if turn.N == n {
						e.t.Logf("turn %d of %s ended with an error: %s\n%s", n, ref, turn.Error, e.runnerLog(last.Session.ID))
					}
				}
				return st, res
			case "ok", "stopped":
				return st, res
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("turn %d of %s never finished; last show session=%#v turns=%#v\n%s", n, ref, last.Session, last.Turns, e.runnerLog(last.Session.ID))
		}
		time.Sleep(e2ePoll)
	}
}

// runnerLog is the tail of a session's daemon.log, where its turn runners
// write their errors, for failure messages.
func (e *e2e) runnerLog(id string) string {
	b, err := os.ReadFile(filepath.Join(e.data, "dyna", "sessions", id, "daemon.log"))
	if err != nil {
		return "daemon.log: " + err.Error()
	}
	if len(b) > 4096 {
		b = b[len(b)-4096:]
	}
	return "daemon.log tail:\n" + string(b)
}

// waitTurnRunning polls show until turn n's record says it is running. A
// turn that ends first fails with its record, so a turn cut short is not
// mistaken for one that never started.
func (e *e2e) waitTurnRunning(ref string, n int) {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		last := e.show(ref)
		for _, turn := range last.Turns {
			if turn.N != n {
				continue
			}
			switch turn.Status {
			case "running":
				return
			case "ok", "error", "stopped":
				e.t.Fatalf("turn %d of %s ended before the test saw it run: %+v\n%s", n, ref, turn, e.runnerLog(last.Session.ID))
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("turn %d of %s never ran; last show session=%#v turns=%#v\n%s", n, ref, last.Session, last.Turns, e.runnerLog(last.Session.ID))
		}
		time.Sleep(e2ePoll)
	}
}

// waitRoomText polls `dyna room log <room> --json` until a post to the
// owner contains substr. Top-level completions land here.
func (e *e2e) waitRoomText(roomName, substr string) {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last string
	for {
		last = e.must("room", "log", roomName, "--json")
		if strings.Contains(last, substr) {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("room %s never showed %q; last:\n%s", roomName, substr, last)
		}
		time.Sleep(e2ePoll)
	}
}

// waitTurnReleased waits for final runner writes after a terminal turn record.
func (e *e2e) waitTurnReleased(id string) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.t.Context(), 30*time.Second)
	defer cancel()
	unlock, err := store.LockContext(ctx, filepath.Join(e.data, "dyna", "sessions", id, "turn.lock"))
	if err != nil {
		e.t.Fatalf("runner did not release its turn lock: %v", err)
	}
	unlock()
}

// The prepared schema must work without schema ownership, and each fixture
// must get an independent database file and connection for mutable room data.
func TestE2EFixturesUseIndependentPreparedSchemas(t *testing.T) {
	dynaBinary(t)
	owned := database.OwnsSchema()
	database.DisownSchema()
	t.Cleanup(func() {
		if owned {
			database.OwnSchema()
		}
	})
	first := e2eData(t)
	firstDB, err := database.Open(filepath.Join(first, "dyna"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(filepath.Join(first, "dyna")); err != nil {
			t.Error(err)
		}
	})
	firstRooms := &room.Store{Root: filepath.Join(first, "dyna", "rooms")}
	firstRoom, err := firstRooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	// Copy after the write, so mutable fixture data cannot leak back into
	// the prepared image. The second copy must still contain no rooms.
	second := e2eData(t)
	secondDB, err := database.Open(filepath.Join(second, "dyna"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(filepath.Join(second, "dyna")); err != nil {
			t.Error(err)
		}
	})
	if firstDB == secondDB {
		t.Fatal("fixtures share a database connection")
	}
	secondRooms := &room.Store{Root: filepath.Join(second, "dyna", "rooms")}
	if rooms, err := secondRooms.List(false); err != nil || len(rooms) != 0 {
		t.Fatalf("second fixture inherited rooms: %+v, %v", rooms, err)
	}
	if rooms, err := firstRooms.List(false); err != nil || len(rooms) != 1 || rooms[0].ID != firstRoom.ID {
		t.Fatalf("copying changed first fixture rooms: %+v, %v", rooms, err)
	}
}

func TestSubagentCLILifecycleEndToEnd(t *testing.T) {
	e := newE2E(t)
	t.Chdir(e.dir)
	// Start through the binary, then exercise the same registered commands
	// directly. They still use the engine socket and real detached runners,
	// without a new CLI process per assertion.
	run := func(args ...string) (string, string, error) {
		e.t.Helper()
		ctx, cancel := context.WithTimeout(e.t.Context(), 30*time.Second)
		defer cancel()
		cmd := newRootCommand()
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(ctx)
		return stdout.String(), stderr.String(), err
	}
	must := func(args ...string) string {
		e.t.Helper()
		stdout, stderr, err := run(args...)
		if err != nil {
			e.t.Fatalf("dyna %s: %v\nstdout=%s\nstderr=%s", strings.Join(args, " "), err, stdout, stderr)
		}
		return stdout
	}
	// Runner and CLI writes commit in the fixture engine. Coalesce their
	// notifications, then read the current turn instead of polling the CLI.
	db, err := database.Open(filepath.Join(e.data, "dyna"))
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan struct{}, 1)
	runningCommitted, allowAcceptance := make(chan struct{}), make(chan struct{})
	releaseAcceptance := sync.OnceFunc(func() { close(allowAcceptance) })
	t.Cleanup(releaseAcceptance)
	var holdRunning sync.Once
	state.For(db).SetPublish(func(ev hub.Event) {
		e.events.Publish(ev)
		select {
		case changed <- struct{}{}:
		default:
		}
		if ev.Topic != "sessions" {
			return
		}
		var raw []byte
		if err := db.QueryRow("SELECT turn FROM session_turns WHERE session = ? AND n = 3", ev.Key).Scan(&raw); err != nil {
			return
		}
		var turn subagent.Turn
		if err := json.Unmarshal(raw, &turn); err != nil {
			t.Errorf("decode committed turn: %v", err)
			return
		}
		if turn.Status == subagent.TurnRunning && !turn.HeldAccepted {
			// Hold the running write's acknowledgement. The runner cannot
			// accept its startup prompt until this publication returns.
			holdRunning.Do(func() {
				close(runningCommitted)
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				select {
				case <-allowAcceptance:
				case <-ctx.Done():
					t.Error("running-phase wait did not release startup acceptance")
				}
			})
		}
	})
	t.Cleanup(func() { state.For(db).SetPublish(e.events.Publish) })

	// start prints the full address at once and runs the turn in the
	// background. With no explicit room the session lands in the repo's room.
	addr := strings.TrimSpace(e.must("subagent", "start", "--goal", "Finish the test task", "--profile", "mock", "--name", "e2e", "--dir", t.TempDir(), "RESPOND: first job SLEEP:60000"))
	if addr != "work:e2e" {
		t.Fatalf("start printed %q, want the full address work:e2e", addr)
	}
	sessionStore := &subagent.Store{Root: filepath.Join(e.data, "dyna", "sessions")}
	info, err := sessionStore.Resolve(addr)
	if err != nil {
		t.Fatalf("resolve started session %q: %v", addr, err)
	}
	id := info.ID
	if !strings.HasPrefix(id, "ses_") {
		t.Fatalf("session id = %q", id)
	}
	// Each phase gets a fresh failure bound. Running phases wait for harness
	// acceptance; the running row alone can precede OnStart. Terminal phases
	// wait for the completion report before an idle post.
	waitTurn := func(n int, status string) subagent.Turn {
		e.t.Helper()
		ctx, cancel := context.WithTimeout(e.t.Context(), 30*time.Second)
		defer cancel()
		for {
			turn, err := sessionStore.ReadTurn(id, n)
			if err != nil {
				e.t.Fatal(err)
			}
			ready := turn.Reported
			if status == subagent.TurnRunning {
				ready = turn.HeldAccepted
			}
			if turn.Status == status && ready {
				return turn
			}
			if turn.Done() && turn.Status != status {
				e.t.Fatalf("turn %d reached %s, want %s: %+v\n%s", n, turn.Status, status, turn, e.runnerLog(id))
			}
			if n == 3 && status == subagent.TurnRunning && !turn.HeldAccepted {
				releaseAcceptance()
			}
			select {
			case <-changed:
			case <-ctx.Done():
				e.t.Fatalf("turn %d never reached %s: %+v\n%s", n, status, turn, e.runnerLog(id))
			}
		}
	}
	if !t.Run("messages_and_follow_up", func(t *testing.T) {
		parentTest := e.t
		e.t = t
		defer func() { e.t = parentTest }()
		if out := must("subagent", "list"); !strings.Contains(out, id) || !strings.Contains(out, "e2e") {
			t.Fatalf("list = %q", out)
		}

		// Running means startup has drained the prompt and the live steering
		// source owns every newer post. Steering only after that point always
		// reaches the running mock turn, which then ends at once. The long SLEEP
		// is only a safety deadline.
		waitTurn(1, subagent.TurnRunning)
		// Steer by full address while the turn runs.
		msgOut := must("post", "@"+addr, "take the left branch")
		if !strings.Contains(msgOut, addr+": session-steered") && !strings.Contains(msgOut, addr+": session-queued") {
			t.Fatalf("msg output = %q", msgOut)
		}
		// The delivered steering reaches the worker inside a [dyna message]
		// envelope, so the mock echoes the envelope around the text.
		first := waitTurn(1, subagent.TurnOK)
		if result, ok := first.Result.(string); !ok || !strings.Contains(result, "take the left branch") {
			t.Fatalf("turn 1 result = %#v", first.Result)
		}
		// Both startup prompt delivery and live steering are valid. In either case,
		// exactly one stored post must have reached the worker on this turn.
		if len(first.Steering) != 1 || !first.Steering[0].Delivered || first.Steering[0].ID == "" || !strings.Contains(first.Steering[0].Message, "take the left branch") {
			t.Fatalf("first turn steering = %+v", first.Steering)
		}
		// A top-level session posts its completion to its room.
		if out := must("room", "log", "work", "--json"); !strings.Contains(out, addr+" turn 1 completed") {
			t.Fatalf("room completion = %q", out)
		}

		// Idle posts wait for the engine to start a follow-up in the same session.
		if out := must("post", "@"+addr, "RESPOND: second answer SLEEP:0"); !strings.Contains(out, ": session-queued") {
			t.Fatalf("follow-up msg output = %q", out)
		}
		e.deliverSessionPosts()
		// The follow-up prompt is the framed message, so the mock's RESPOND
		// echo carries the envelope's trailing "Explore:" line; the answer
		// itself leads.
		second := waitTurn(2, subagent.TurnOK)
		if result, ok := second.Result.(string); !ok || !strings.HasPrefix(result, "second answer") {
			t.Fatalf("turn 2 result = %#v", second.Result)
		}
		if out := strings.TrimSpace(must("subagent", "output", addr)); !strings.HasPrefix(out, "second answer") {
			t.Fatalf("output = %q", out)
		}
		if out := must("subagent", "output", addr, "--turn", "1", "--raw"); !strings.Contains(out, "take the left branch") {
			t.Fatalf("raw output = %q", out)
		}
		e.waitTurnReleased(id)
		var show showJSON
		if err := json.Unmarshal([]byte(must("subagent", "show", id, "--json")), &show); err != nil {
			t.Fatal(err)
		}
		if show.Session.Status != "idle" || show.Session.Turns != 2 || len(show.Turns) != 2 ||
			show.Session.Name != "e2e" || show.Session.Room != "work" ||
			show.Turns[0].HarnessSession == "" || show.Turns[0].HarnessSession != show.Turns[1].HarnessSession {
			t.Fatalf("show = %#v", show)
		}
		// show reads as a conversation: turn separators, the prompt, steering with
		// its delivery state, and replies; never raw records.
		if out := must("subagent", "show", addr); strings.Contains(out, "journal") || !strings.Contains(out, "── turn 2 · ok") ||
			!strings.Contains(out, "↪ steering") || !strings.Contains(out, "✓ delivered") || !strings.Contains(out, "◆ mock") ||
			!strings.Contains(out, "second answer") || strings.Contains(out, `{"`) {
			t.Fatalf("show text = %q", out)
		}
		if out := must("subagent", "output", addr, "--turn", "1", "--transcript"); !strings.Contains(out, "── turn 1 · ok") || !strings.Contains(out, "take the left branch") {
			t.Fatalf("output --transcript = %q", out)
		}
		if _, _, err := run("subagent", "stop", addr); err == nil {
			t.Fatal("stop on an idle session succeeded")
		}

	}) {
		return
	}

	t.Run("stop_resume_and_close", func(t *testing.T) {
		parentTest := e.t
		e.t = t
		defer func() { e.t = parentTest }()
		// Stop a long turn; the session stays resumable.
		must("post", "@"+addr, "long SLEEP:60000")
		e.deliverSessionPosts()
		phase, cancelPhase := context.WithTimeout(t.Context(), 30*time.Second)
		select {
		case <-runningCommitted:
		case <-phase.Done():
			cancelPhase()
			t.Fatal("turn 3 did not publish its running state")
		}
		cancelPhase()
		if turn := waitTurn(3, subagent.TurnRunning); !turn.HeldAccepted {
			t.Fatal("running-phase wait returned before startup acceptance")
		}
		if out := must("subagent", "stop", addr); !strings.Contains(out, "stopped turn 3") {
			t.Fatalf("stop output = %q", out)
		}
		waitTurn(3, subagent.TurnStopped)
		if _, _, err := run("subagent", "output", addr, "--turn", "3"); err == nil {
			t.Fatal("output of a stopped turn should exit non-zero")
		}
		must("post", "@"+addr, "RESPOND: resumed after stop SLEEP:0")
		e.deliverSessionPosts()
		resumed := waitTurn(4, subagent.TurnOK)
		if result, ok := resumed.Result.(string); !ok || !strings.HasPrefix(result, "resumed after stop") {
			t.Fatalf("after-stop turn result = %#v", resumed.Result)
		}

		e.waitTurnReleased(id)
		must("subagent", "close", addr)
		if out := must("subagent", "list"); strings.Contains(out, id) {
			t.Fatalf("closed session in default list: %q", out)
		}
		if out := must("subagent", "list", "--all"); !strings.Contains(out, id) || !strings.Contains(out, "closed") {
			t.Fatalf("list --all = %q", out)
		}
		// Messaging a closed session is refused: addresses resolve only while
		// the session is open, so the send finds no live recipient.
		if _, stderr, err := run("post", "@"+addr, "more"); err == nil || !strings.Contains(stderr, "no open session named") {
			t.Fatalf("msg to closed session: %v %s", err, stderr)
		}
	})

}

// The deleted waiting/sending surface stays deleted.
func TestDeletedPublicCommandsAreGone(t *testing.T) {
	e := newE2E(t)
	for _, args := range [][]string{
		{"subagent", "wait", "work:x"},
		{"subagent", "send", "work:x", "hi"},
		{"runs", "wait", "wf_x"},
		{"runs", "steer", "wf_x", "1", "hi"},
		{"events"},
		{"send", "work:x", "hi"},
	} {
		_, stderr, err := e.run(args...)
		if err == nil || !strings.Contains(stderr, "unknown command") {
			t.Fatalf("dyna %s: err=%v stderr=%q, want unknown command", strings.Join(args, " "), err, stderr)
		}
	}
	if _, stderr, err := e.run("subagent", "start", "--goal", "Finish the test task", "--profile", "mock", "--wait", "x"); err == nil || !strings.Contains(stderr, "unknown flag") {
		t.Fatalf("subagent start --wait: err=%v stderr=%q, want unknown flag", err, stderr)
	}
}

// roomEvents streams the fixture room's stored posts, including agent-only
// completions. The history window also covers posts written before startup.
func (e *e2e) roomEvents(roomName string) <-chan room.Message {
	t := e.t
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, e.bin, "room", "watch", roomName, "--since", "15m", "--all")
	cmd.Env = e.env()
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := e.startCLI(ctx, cmd); err != nil {
		cancel()
		t.Fatal(err)
	}
	events := make(chan room.Message, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			var msg room.Message
			if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
				t.Errorf("room event: %v", err)
				return
			}
			select {
			case events <- msg:
			case <-ctx.Done():
				return
			}
		}
		if err := sc.Err(); err != nil && ctx.Err() == nil {
			t.Errorf("room watch: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); _ = cmd.Wait(); <-done })
	return events
}

// A Dyna subagent starts and messages its child.
// Automatic child completion is queued for its parent. The engine delivers
// it with later child traffic, and the child-woken parent turn ends silently.
func TestSubagentChildCompletionReportsToParentInExplicitRoom(t *testing.T) {
	work := t.TempDir()
	script := `#!/bin/sh
set -eu
input=$(cat)
# Sessions get no work journal.
[ -z "${DYNA_AGENT_JOURNAL:-}" ] || { echo "session got a journal: $DYNA_AGENT_JOURNAL" >&2; exit 3; }
case " $* " in
  *" --resume "*)
    printf '%s' "$input" > "$WORK/parent-followup-input"
    printf '%s\n' 'parent received explicit child post'
    ;;
  *)
    child=$("$DYNA_BIN" subagent start --goal 'Check the left branch' --profile mock --name decoder --dir "$WORK" 'RESPOND: child job SLEEP:60000')
    printf '%s' "$child" > "$WORK/child-address"
    printf '%s\n' 'child started'
    ;;
esac
`
	e := newE2E(t,
		profile.Profile{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Default: true},
		profile.Profile{
			Name: "parent", Harness: profile.HarnessClaudeCode, Taste: 5, Intelligence: 5, Cost: 5,
			Env: map[string]string{"WORK": work},
		},
	)
	e.installClaudeStream(script)

	// The repo names the room. Creating it again from the same repo returns
	// the same room, so an agent can rediscover it without storing a name.
	repo := filepath.Join(t.TempDir(), "E2E_Repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	roomName := strings.TrimSpace(e.must("room", "create", "--repo", repo))
	if roomName != "e2e-repo" {
		t.Fatalf("room create printed %q", roomName)
	}
	if again := strings.TrimSpace(e.must("room", "create", "--repo", repo)); again != roomName {
		t.Fatalf("second room create printed %q, want %q", again, roomName)
	}

	events := e.roomEvents(roomName)

	var parent startResult
	var childState showJSON
	childAddr := roomName + ":decoder"
	parentTest := e.t
	if !t.Run("automatic_completion_reaches_parent", func(t *testing.T) {
		e.t = t
		defer func() { e.t = parentTest }()
		parent = e.startSession("--profile", "parent", "--name", "lead", "--room", roomName, "--dir", work, "delegate to a child session")
		if parent.Address != roomName+":lead" || parent.Room != roomName {
			t.Fatalf("parent start = %#v", parent)
		}
		// Both terminal states are announced by the watcher already running.
		// Wait on those posts before inspecting each session once. The report
		// follows the runner's turn-end write. The timeout is only the failure
		// bound: t.Context() itself ends after this function returns, so a
		// missing route would otherwise hold the suite until the package timeout.
		fail, cancelFail := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancelFail()
		seen := map[string]bool{}
		for len(seen) < 2 {
			select {
			case msg, ok := <-events:
				if !ok {
					t.Fatal("room watch ended early")
				}
				if msg.Kind == "completed" && (msg.From == parent.Address || msg.From == childAddr) {
					to, via := roomName, "@root"
					if msg.From == childAddr {
						to, via = parent.Address, "@parent"
					}
					if msg.Turn != 1 || len(msg.To) != 1 || msg.To[0] != to || len(msg.Via) != 1 || msg.Via[0] != via {
						t.Fatalf("automatic completion has the wrong report recipient: %+v", msg)
					}
					seen[msg.From] = true
					if msg.From == parent.Address {
						// Release the child only after the parent's result is sealed.
						// Otherwise its completion can steer the parent's first call.
						out, stderr, err := e.runEnv([]string{
							"DYNA_MODE=subagent", "DYNA_SESSION=" + parent.Session, "DYNA_DEPTH=1",
							"DYNA_ROOM=" + roomName, "DYNA_ROOM_ID=" + parent.RoomID,
						}, "post", "@"+childAddr, "check the left branch")
						if err != nil || !strings.Contains(out, "session-queued") {
							t.Fatalf("parent post to held child: %s %s %v", out, stderr, err)
						}
					}
				}
			case <-fail.Done():
				t.Fatalf("room watch did not show both completion routes: seen=%v", seen)
			}
		}
		childAddrBytes, err := os.ReadFile(filepath.Join(work, "child-address"))
		if err != nil {
			t.Fatal(err)
		}
		reportedChildAddr := strings.TrimSpace(string(childAddrBytes))
		if reportedChildAddr != childAddr {
			t.Fatalf("child address = %q, want %q", reportedChildAddr, childAddr)
		}
		childState = e.show(childAddr)
		if status, result, ok := childState.turn(1); !ok || status != "ok" || !strings.Contains(result, "check the left branch") {
			t.Fatalf("child turn 1 = %s %q", status, result)
		}
		if childState.Session.Turns != 1 || len(childState.Turns) != 1 || len(childState.Turns[0].Steering) != 1 {
			t.Fatalf("child must consume one post on turn 1: %+v", childState)
		}
		delivered := childState.Turns[0].Steering[0]
		if delivered.ID == "" || !delivered.Delivered || !strings.Contains(delivered.Message, "check the left branch") {
			t.Fatalf("child post was not delivered exactly once on turn 1: %+v", delivered)
		}
		if out := e.must("room", "members", roomName); !strings.Contains(out, parent.Address) || !strings.Contains(out, childAddr) {
			t.Fatalf("room members = %q", out)
		}

		parentState := e.show(parent.Address)
		if status, result, ok := parentState.turn(1); !ok || status != "ok" || result != "child started" {
			t.Fatalf("parent turn 1 = %s %q", status, result)
		}
		if parentState.Session.Turns != 1 {
			t.Fatalf("queued completion started a new turn without an engine pass: %+v", parentState)
		}
		if _, err := os.Stat(filepath.Join(work, "parent-followup-input")); !os.IsNotExist(err) {
			t.Fatalf("automatic completion started a parent follow-up: %v", err)
		}

	}) {
		return
	}
	t.Run("explicit_child_post_wakes_parent_without_a_report", func(t *testing.T) {
		e.t = t
		defer func() { e.t = parentTest }()
		// Explicit child messages keep their normal routing and wake behavior.
		_, stderr, err := e.runEnv([]string{
			"DYNA_MODE=subagent", "DYNA_SESSION=" + childState.Session.ID, "DYNA_DEPTH=2",
			"DYNA_ROOM=" + roomName, "DYNA_ROOM_ID=" + parent.RoomID, "DYNA_ADDRESS=" + childAddr,
		}, "post", "@parent", "ready for review")
		if err != nil {
			t.Fatalf("explicit @parent post: %v: %s", err, stderr)
		}
		e.deliverSessionPosts()
		if status, result := e.waitTurn(parent.Address, 2); status != "ok" || result != "parent received explicit child post" {
			t.Fatalf("parent turn 2 = %s %q", status, result)
		}
		followup, err := os.ReadFile(filepath.Join(work, "parent-followup-input"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"from " + childAddr + "]", "To: @parent = " + parent.Address + " (you)", "ready for review"} {
			if !strings.Contains(string(followup), want) {
				t.Fatalf("parent follow-up input missing %q:\n%s", want, followup)
			}
		}
		if !strings.Contains(string(followup), childAddr+" turn 1 completed") {
			t.Fatalf("parent input missed the queued child completion: %s", followup)
		}
		e.waitReported(parent.Address, 2)
		if log := e.must("room", "log", roomName, "--all"); strings.Contains(log, parent.Address+" turn 2 ") {
			t.Fatalf("room log has a report for the child-woken parent turn:\n%s", log)
		}
	})
}

// A detached runner keeps old launch stamps after promotion. Its later CLI
// calls must still use the current parent and depth from the same store.
func TestPromotionCLIUsesStoredHierarchyForRunningAgents(t *testing.T) {
	// The worker connects only after its harness starts, then blocks until
	// the test releases it. Promotion cannot finish the turn as mock steering does.
	gate, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gate.Close() })
	e := newE2E(t,
		profile.Profile{Name: "mock", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Default: true},
		profile.Profile{Name: "held", Harness: profile.HarnessClaudeCode, Taste: 5, Intelligence: 5, Cost: 5,
			Env: map[string]string{"PROMOTION_GATE": gate.Addr().String()}},
	)
	e.installClaudeStream(`#!/bin/sh
set -eu
python3 - <<'PYTHON'
import os, socket
host, port = os.environ["PROMOTION_GATE"].split(":")
with socket.create_connection((host, int(port))) as gate:
    assert gate.recv(1) == b"x"
print("held turn completed")
PYTHON
`)
	lead := e.startSession("--name", "lead", "RESPOND: lead SLEEP:0")
	e.waitTurn(lead.Address, 1)
	stamp := func(parent startResult, depth string) []string {
		return []string{"DYNA_MODE=subagent", "DYNA_SESSION=" + parent.Session, "DYNA_DEPTH=" + depth, "DYNA_ROOM_ID=" + parent.RoomID, "DYNA_ROOM=" + parent.Room}
	}
	e.extra = stamp(lead, "1")
	child := e.startSession("--profile", "held", "--name", "child", "job")
	e.extra = nil
	t.Cleanup(func() { e.run("subagent", "close", child.Session) })
	worker := acceptChildHarness(t, e, gate, child.Session)
	defer worker.Close()
	sessions := &subagent.Store{Root: filepath.Join(e.data, "dyna", "sessions")}
	meta, err := sessions.ReadMeta(child.Session)
	if err != nil {
		t.Fatal(err)
	}
	e.must("subagent", "promote", child.Session)
	current := e.show(child.Session).Session
	if current.ParentSession != "" || current.Depth != 1 || current.Turns != 1 || current.Status != "running" {
		t.Fatalf("promoted running session = %+v", current)
	}
	out, stderr, err := e.runEnv(stamp(child, "3"), "post", "@parent", "The promoted agent reports to the owner.")
	if err != nil || !strings.Contains(out, "work: room") {
		t.Fatalf("promoted parent selector: %s %s %v", out, stderr, err)
	}
	e.extra = stamp(child, "3")
	grandchild := e.startSession("--name", "grandchild", "RESPOND: grandchild SLEEP:0")
	e.extra = nil
	e.waitTurn(grandchild.Session, 1)
	if got := e.show(grandchild.Session).Session; got.Depth != 2 || got.ParentSession != child.Session {
		t.Fatalf("child creation from stale stamp = %+v", got)
	}
	if _, err := worker.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := waitE2ERunner(ctx, meta.Pid, "subagent _turn "+child.Session+" 1"); err != nil {
		t.Fatalf("child runner did not finish: %v", err)
	}
	if status, result, _ := e.show(child.Session).turn(1); status != "ok" || result != "held turn completed" {
		t.Fatalf("promotion interrupted work: %s %q", status, result)
	}
	e.must("subagent", "close", child.Session)
	if got := e.show(grandchild.Session).Session; got.Depth != 1 || got.ParentSession != "" || got.Status == "closed" {
		t.Fatalf("close hand-over = %+v", got)
	}
}

// acceptGuard bounds a hung harness start. It is a failure bound, not a wait.
const acceptGuard = time.Minute

// acceptChildHarness waits for the child's harness to connect, or for its
// first turn to end without connecting. It waits on those events, not on a
// fixed deadline: a loaded machine can take seconds to start the detached
// runner and its harness. A start that hangs fails after acceptGuard, far
// above any load-induced delay, rather than at go test's 10-minute timeout.
func acceptChildHarness(t *testing.T, e *e2e, gate *net.TCPListener, session string) *net.TCPConn {
	t.Helper()
	type accepted struct {
		conn *net.TCPConn
		err  error
	}
	got := make(chan accepted, 1)
	go func() {
		conn, err := gate.AcceptTCP()
		got <- accepted{conn, err}
	}()
	tick := time.NewTicker(e2ePoll)
	defer tick.Stop()
	guard := time.After(acceptGuard)
	for {
		select {
		case <-guard:
			t.Fatalf("child harness did not connect within %v while turn 1 kept running", acceptGuard)
		case a := <-got:
			if a.err != nil {
				t.Fatalf("child harness did not start: %v", a.err)
			}
			return a.conn
		case <-tick.C:
			if status, result, ok := e.show(session).turn(1); ok && status != "queued" && status != "running" {
				t.Fatalf("child turn 1 ended before its harness connected: %s %q", status, result)
			}
		}
	}
}

// installClaudeStream wraps a shell worker in Claude's persistent-input protocol.
// The worker sees plain prompt text and closed stdin. The wrapper acknowledges
// live input and keeps the terminal result current until Dyna retires it.
func (e *e2e) installClaudeStream(script string) {
	t := e.t
	t.Helper()
	dir := t.TempDir()
	worker := filepath.Join(dir, "claude-worker")
	if err := os.WriteFile(worker, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	wrapper := `#!/usr/bin/env python3
import json, os, subprocess, sys, threading
if "--version" in sys.argv:
    print("test-1.0.0")
    sys.exit(0)
record = json.loads(sys.stdin.readline())
content = record["message"]["content"]
text = content if isinstance(content, str) else "".join(part.get("text", "") for part in content)
lock = threading.Lock()
terminal = None
def replay(record):
    record["isReplay"] = True
    print(json.dumps(record), flush=True)
def acknowledge():
    for line in sys.stdin:
        with lock:
            replay(json.loads(line))
            if terminal is not None:
                print(json.dumps(terminal), flush=True)
replay(record)
reader = threading.Thread(target=acknowledge, daemon=True)
reader.start()
worker = os.path.join(os.path.dirname(__file__), "claude-worker")
result = subprocess.run(["/bin/sh", worker, *sys.argv[1:]], input=text, text=True, capture_output=True)
sys.stderr.write(result.stderr)
with lock:
    terminal = {"type": "result", "result": result.stdout, "is_error": result.returncode != 0}
    print(json.dumps(terminal), flush=True)
# Keep accepting late steering until Dyna seals input and retires the worker.
reader.join()
sys.exit(result.returncode)
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	fakeruntime.InstallClaude(t, filepath.Join(e.data, "dyna"), filepath.Join(dir, "claude"))
}

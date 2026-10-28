package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/textutil"
)

const (
	// defaultMaxCommands is how many run() commands of one run execute at once.
	defaultMaxCommands = 4
	// defaultCommandTimeout bounds a command whose script sets no timeout.
	defaultCommandTimeout = 5 * time.Hour
	// commandInlineLimit caps the stdout and stderr a run() result carries. The
	// whole log stays in the shell's log file (logPath).
	commandInlineLimit = 64 * 1024
	// commandParseLimit caps the output that parse: "json" reads.
	commandParseLimit = 16 * 1024 * 1024
	// commandTailLines and commandTailBytes bound the log tail of an exec_end event.
	commandTailLines = 20
	commandTailBytes = 4000
	// commandStopWait is how long a stopped command may take to record its exit.
	commandStopWait = 10 * time.Second
)

// CommandSpec describes one command for a CommandRunner.
type CommandSpec struct {
	// Argv is the program and its arguments, run without a shell.
	Argv []string
	Cwd  string
	// Env holds KEY=value pairs added to the run's environment.
	Env []string
	// Name says what the command does, for the shell list.
	Name string
	// StderrPath receives the command's standard error. The shell log holds
	// only standard output, so a script can parse stdout apart from warnings.
	StderrPath string
}

// CommandExit is how a command ended.
type CommandExit struct {
	Code   int
	Signal string
	// Error is a failure to run the command at all (not a non-zero exit).
	Error string
	// LogPath is the file that holds the command's standard output (and the
	// log a person reads in the shell list).
	LogPath string
}

// CommandRunner runs the commands of run() calls as managed shells. The real
// one is backed by internal/shells; tests pass a fake.
type CommandRunner interface {
	// Start runs the command and returns its shell identifier at once.
	Start(ctx context.Context, spec CommandSpec) (string, error)
	// Attach claims a shell that the run being resumed started, so this run
	// can wait for it and stop it. It fails for any other shell.
	Attach(ctx context.Context, shell string) error
	// Wait blocks until the shell ends or ctx ends.
	Wait(ctx context.Context, shell string) (CommandExit, error)
	// Output reads the first limit bytes of the command's standard output.
	Output(shell string, limit int64) (string, error)
	// Stop ends a running shell. It is safe to call for one that ended.
	Stop(shell string) error
}

// execRecord is what a resumed run knows of one earlier run() call.
type execRecord struct {
	Shell  string
	Stderr string // where the original run's command writes its standard error
	Done   bool
	Result any
}

// commandKey identifies a run() call across a resume. check is part of it: a
// result accepted with check: false must not satisfy a call that rejects it.
// Timeouts and retries do not matter: they decide how long to wait, not what runs.
func commandKey(argv []string, cwd, label, parse string, check bool, env []string) string {
	sorted := append([]string(nil), env...)
	sort.Strings(sorted)
	payload, _ := json.Marshal([]any{argv, cwd, label, parse, check, sorted})
	h := sha256.Sum256(payload)
	return "x1:" + hex.EncodeToString(h[:])
}

// commandLine renders argv for the run view. An argument array shows as it
// would be typed.
func commandLine(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\$") {
			parts[i] = fmt.Sprintf("%q", a)
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

// tailText returns the last lines of s, within the line and byte limits.
func tailText(s string) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > commandTailLines {
		lines = lines[len(lines)-commandTailLines:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > commandTailBytes {
		out = out[len(out)-commandTailBytes:]
	}
	return strings.ToValidUTF8(out, "")
}

// clipInline cuts s to the inline limit on a character boundary.
func clipInline(s string) (string, bool) {
	if len(s) <= commandInlineLimit {
		return s, false
	}
	return strings.ToValidUTF8(s[:commandInlineLimit], ""), true
}

// readCapped reads at most limit bytes of the file at path. A missing file is
// empty output.
func readCapped(path string, limit int64) (string, error) {
	if path == "" {
		return "", nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit))
	return string(b), err
}

type commandOptions struct {
	cwd, label, parse, phase, lane, stage, planNodeID string
	check                                             bool
	timeout                                           time.Duration
	retries                                           int
	env                                               []string
	group                                             string
	stageIndex, itemIndex                             int
	hasStage, hasItem                                 bool
}

// parseCommandCall reads the arguments of run(command, opts).
func (e *engine) parseCommandCall(call goja.FunctionCall) ([]string, commandOptions, error) {
	o := commandOptions{cwd: e.opts.WorkDir, check: true, timeout: defaultCommandTimeout}
	var argv []string
	switch v := call.Argument(0).Export().(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, o, errors.New("run() needs a command")
		}
		argv = []string{"sh", "-c", v}
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, o, errors.New("run() arguments must be strings")
			}
			argv = append(argv, s)
		}
		if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
			return nil, o, errors.New("run() needs a program name as the first argument")
		}
	default:
		return nil, o, errors.New("run() needs a command string or an array of arguments")
	}
	opts, _ := call.Argument(1).Export().(map[string]any)
	str := func(key string) string { s, _ := opts[key].(string); return s }
	o.label, o.parse, o.phase, o.lane, o.stage = str("label"), str("parse"), str("phase"), str("lane"), str("stage")
	o.planNodeID, o.group = str("__planNodeId"), str("__planGroupId")
	if v := str("cwd"); v != "" {
		o.cwd = v
	}
	if o.parse != "" && o.parse != "json" {
		return nil, o, fmt.Errorf("run() parse must be \"json\", not %q", o.parse)
	}
	if v, ok := opts["check"].(bool); ok {
		o.check = v
	}
	if v, ok := opts["timeout"]; ok {
		f, ok := toFloat(v)
		if !ok || f <= 0 {
			return nil, o, errors.New("run() timeout must be a positive number of seconds")
		}
		o.timeout = time.Duration(f * float64(time.Second))
	}
	if v, ok := opts["retries"]; ok {
		f, ok := toFloat(v)
		if !ok || f < 0 || f != float64(int(f)) {
			return nil, o, errors.New("run() retries must be a whole number, zero or more")
		}
		o.retries = int(f)
	}
	if raw, ok := opts["env"]; ok && raw != nil {
		m, isMap := raw.(map[string]any)
		if !isMap {
			return nil, o, errors.New("run() env must be an object of strings")
		}
		for k, v := range m {
			s, isStr := v.(string)
			if !isStr || k == "" || strings.ContainsAny(k, "=\x00") {
				return nil, o, fmt.Errorf("run() env %q must be a string value under a plain name", k)
			}
			o.env = append(o.env, k+"="+s)
		}
		sort.Strings(o.env)
	}
	if v, ok := optInt(opts, "__stageIndex"); ok {
		o.stageIndex, o.hasStage = v, true
	}
	if v, ok := optInt(opts, "__itemIndex"); ok {
		o.itemIndex, o.hasItem = v, true
	}
	abs, err := filepath.Abs(o.cwd)
	if err != nil {
		return nil, o, fmt.Errorf("resolve command cwd: %w", err)
	}
	o.cwd = abs
	return argv, o, nil
}

// execResult is the value a run() promise resolves with.
func execResult(code int, stdout, stderr string, parsed any, dur time.Duration, attempts int, shell, logPath string) map[string]any {
	return map[string]any{
		"code": code, "stdout": stdout, "stderr": stderr, "json": parsed,
		"durationMs": dur.Milliseconds(), "attempts": attempts, "shellId": shell, "logPath": logPath,
	}
}

// exec implements one run() call: a deterministic command that runs as a
// managed shell, with no model turn. Called on the loop thread; returns a
// Promise resolved from a worker goroutine.
func (e *engine) exec(call goja.FunctionCall) goja.Value {
	vm := e.vm
	promise, resolveFn, rejectFn := vm.NewPromise()
	resolve := func(v any) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) { resolveFn(vm.ToValue(v)) })
	}
	reject := func(err error) {
		e.loop.RunOnLoop(func(vm *goja.Runtime) { rejectFn(vm.NewGoError(err)) })
	}
	argv, o, err := e.parseCommandCall(call)
	if err == nil && e.opts.Commands == nil {
		err = errors.New("this run cannot run commands")
	}
	if err != nil {
		reject(err)
		return vm.ToValue(promise)
	}

	e.shared.mu.Lock()
	e.shared.execSeq++
	id := e.shared.execSeq
	e.shared.mu.Unlock()
	e.phaseMu.Lock()
	if o.phase == "" {
		o.phase = e.curPhase
		if o.lane == "" && o.stage == "" {
			o.lane, o.stage = e.curLane, e.curStage
		}
	}
	e.phaseMu.Unlock()
	cacheLabel := o.label
	label := o.label
	if label == "" {
		label = fmt.Sprintf("command-%d", id)
	}
	key := commandKey(argv, o.cwd, cacheLabel, o.parse, o.check, o.env)
	line := commandLine(argv)
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
		line = argv[2]
	}
	cp := e.snapshotCallPlan(o.planNodeID)
	if o.group != "" {
		cp.groupID = o.group
	}
	if o.hasStage {
		s := o.stageIndex
		cp.stage = &s
	}
	if o.hasItem {
		i := o.itemIndex
		cp.item = &i
	}
	emit := func(ev runstore.Event) {
		cp.apply(&ev)
		ev.Lane, ev.Stage = o.lane, o.stage
		ev.ID, ev.Label, ev.Phase = id, label, o.phase
		e.emit(ev)
	}
	emit(runstore.Event{T: "exec_start", Command: textutil.ClipOneLine(line, 400)})

	if !e.beginWorker() {
		werr := e.ctx.Err()
		if werr == nil {
			werr = errors.New("workflow is already finishing")
		}
		emit(runstore.Event{T: "exec_end", Status: "error", Error: textutil.ClipOneLine(werr.Error(), 2000)})
		reject(werr)
		return vm.ToValue(promise)
	}
	var prior execRecord
	var hasPrior bool
	if e.opts.Cache != nil {
		prior, hasPrior = e.opts.Cache.popExec(key)
	}
	go func() {
		defer e.workers.Done()
		journal := func(state, shell, stderr string, result any, errStr string) {
			if e.opts.Run == nil {
				return
			}
			e.opts.Run.Journal(runstore.JournalEntry{ID: id, Label: label, Key: key, Cwd: o.cwd, Prompt: line, Result: result, Error: errStr,
				Workflow: e.workflowID, Kind: "exec", State: state, Shell: shell, Stderr: stderr})
		}
		// Resume: a finished command gives its recorded result and never runs again.
		if hasPrior && prior.Done {
			emit(runstore.Event{T: "exec_end", Status: "ok", Cached: true, Shell: prior.Shell, Preview: cachedSummary(prior.Result)})
			journal("done", prior.Shell, prior.Stderr, prior.Result, "")
			resolve(prior.Result)
			return
		}
		select {
		case e.shared.cmdSem <- struct{}{}:
		case <-e.ctx.Done():
			emit(runstore.Event{T: "exec_end", Status: "error", Error: e.ctx.Err().Error()})
			reject(e.ctx.Err())
			return
		}
		defer func() { <-e.shared.cmdSem }()
		for e.opts.Paused != nil && e.opts.Paused() {
			select {
			case <-e.ctx.Done():
				emit(runstore.Event{T: "exec_end", Status: "error", Error: e.ctx.Err().Error()})
				reject(e.ctx.Err())
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		// Stop before a replaced binary starts more work (D-402).
		if executableReplaced() {
			e.stopForUpdate(updateReplacementCause)
			emit(runstore.Event{T: "exec_end", Status: "error", Error: updateReplacementCause.Error()})
			reject(updateReplacementCause)
			return
		}
		attach, attachStderr := "", ""
		if hasPrior {
			if aerr := e.opts.Commands.Attach(e.ctx, prior.Shell); aerr != nil {
				e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("%s: cannot reattach to shell %s (%v); starting the command again", label, prior.Shell, aerr))
			} else {
				attach, attachStderr = prior.Shell, prior.Stderr
			}
		}
		start := time.Now()
		var res map[string]any
		var runErr error
		var shell, stderrPath string
		for attempt := 1; ; attempt++ {
			res, shell, stderrPath, runErr = e.execAttempt(argv, o, id, label, line, attempt, attach, attachStderr, journal)
			attach, attachStderr = "", ""
			if runErr == nil || attempt > o.retries || e.ctx.Err() != nil || !retryable(runErr) {
				if res != nil {
					res["attempts"] = attempt
				}
				break
			}
			e.emitLog(runstore.LogProvenanceSystem, fmt.Sprintf("%s: attempt %d failed (%v); trying again", label, attempt, runErr))
		}
		dur := time.Since(start)
		tail := ""
		exitCode := (*int)(nil)
		if res != nil {
			res["durationMs"] = dur.Milliseconds()
			tail = tailText(fmt.Sprint(res["stdout"]) + fmt.Sprint(res["stderr"]))
			c := res["code"].(int)
			exitCode = &c
		}
		if runErr != nil && o.check || runErr != nil && res == nil {
			journal("error", shell, stderrPath, nil, runErr.Error())
			emit(runstore.Event{T: "exec_end", Status: "error", DurMs: dur.Milliseconds(), Error: textutil.ClipOneLine(runErr.Error(), 2000), Shell: shell, ExitCode: exitCode, Tail: tail})
			reject(runErr)
			return
		}
		status := "ok"
		if runErr != nil {
			status = "error" // check: false hands the failing result to the script
		}
		journal("done", shell, stderrPath, res, "")
		emit(runstore.Event{T: "exec_end", Status: status, DurMs: dur.Milliseconds(), Shell: shell, ExitCode: exitCode, Tail: tail, Preview: textutil.ClipOneLine(tail, 160)})
		resolve(res)
	}()
	return vm.ToValue(promise)
}

// failure is an error of a command that ran: a non-zero exit, a timeout or
// output that is not JSON. Only the first two are worth another attempt.
type commandFailure struct {
	msg   string
	retry bool
}

func (f *commandFailure) Error() string { return f.msg }

func retryable(err error) bool {
	var f *commandFailure
	return errors.As(err, &f) && f.retry
}

// execAttempt starts the command (or reattaches to a shell of the earlier run),
// waits for it, and builds the result. The error is set for a failure; the
// result is set whenever the command ran, so check: false can return it.
func (e *engine) execAttempt(argv []string, o commandOptions, id int, label, line string, attempt int, attach, attachStderr string, journal func(state, shell, stderr string, result any, errStr string)) (map[string]any, string, string, error) {
	shell := attach
	stderrPath := attachStderr
	if shell == "" && e.opts.Run != nil && e.opts.Run.Dir != "" {
		dir := filepath.Join(e.opts.Run.Dir, "commands")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, "", "", fmt.Errorf("prepare command logs: %w", err)
		}
		stderrPath = filepath.Join(dir, fmt.Sprintf("%d-%d.stderr", id, attempt))
	}
	if shell == "" {
		var err error
		shell, err = e.opts.Commands.Start(e.ctx, CommandSpec{Argv: argv, Cwd: o.cwd, Env: o.env, Name: "run " + e.runID() + ": " + textutil.ClipOneLine(label, 80), StderrPath: stderrPath})
		if err != nil {
			// State a newer dyna wrote fails every command this binary starts;
			// stop once and let the engine report the interruption (D-402).
			if newerSchemaError(err) {
				e.stopForUpdate(err)
			}
			return nil, "", stderrPath, fmt.Errorf("start command: %w", err)
		}
	}
	journal("running", shell, stderrPath, nil, "")
	e.emit(runstore.Event{T: "exec_run", ID: id, Label: label, Shell: shell, Command: textutil.ClipOneLine(line, 400)})

	start := time.Now()
	wctx, cancel := context.WithTimeout(e.ctx, o.timeout)
	defer cancel()
	exit, err := e.opts.Commands.Wait(wctx, shell)
	timedOut := false
	if err != nil {
		timedOut = errors.Is(wctx.Err(), context.DeadlineExceeded) && e.ctx.Err() == nil
		// The command must not outlive the call that wanted it.
		_ = e.opts.Commands.Stop(shell)
		sctx, scancel := context.WithTimeout(context.WithoutCancel(e.ctx), commandStopWait)
		exit, _ = e.opts.Commands.Wait(sctx, shell)
		scancel()
		if !timedOut {
			return nil, shell, stderrPath, err
		}
	}
	stdout, rerr := e.opts.Commands.Output(shell, commandParseLimit)
	if rerr != nil {
		return nil, shell, stderrPath, fmt.Errorf("read command output: %w", rerr)
	}
	stderr, _ := readCapped(stderrPath, commandInlineLimit)
	inline, _ := clipInline(stdout)
	res := execResult(exit.Code, inline, stderr, nil, time.Since(start), attempt, shell, exit.LogPath)
	switch {
	case timedOut:
		return res, shell, stderrPath, &commandFailure{msg: fmt.Sprintf("command timed out after %s", o.timeout), retry: true}
	case exit.Error != "":
		return res, shell, stderrPath, &commandFailure{msg: "command did not run: " + exit.Error}
	case exit.Signal != "":
		return res, shell, stderrPath, &commandFailure{msg: fmt.Sprintf("command stopped by signal %s", exit.Signal), retry: true}
	case exit.Code != 0:
		return res, shell, stderrPath, &commandFailure{msg: fmt.Sprintf("command exited with code %d: %s", exit.Code, textutil.ClipOneLine(tailText(stderr+stdout), 400)), retry: true}
	}
	if o.parse == "json" {
		var parsed any
		if jerr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed); jerr != nil {
			return res, shell, stderrPath, &commandFailure{msg: fmt.Sprintf("command output is not JSON: %v", jerr)}
		}
		res["json"] = parsed
	}
	return res, shell, stderrPath, nil
}

// newCommandLimit is the counting limit on commands that run at once.
func newCommandLimit(n int) chan struct{} {
	if n <= 0 {
		n = defaultMaxCommands
	}
	return make(chan struct{}, n)
}

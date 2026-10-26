package shellmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/websearch"
)

// ForegroundLimit is how long a command runs before it moves to the background.
const ForegroundLimit = 30 * time.Second

const (
	maxOutput   = 16 << 10
	outputHead  = 4 << 10
	maxWait     = 600 * time.Second
	defaultWait = 30 * time.Second
)

// MaxRequestTimeout is the longest MCP request this server serves: the longest
// wait for a job plus a minute for the call around it. A harness config uses it
// as the server's request timeout, so a wait is not cut off at the transport.
const MaxRequestTimeout = maxWait + time.Minute

// holdGrace is how long a hold outlives its waiter's deadline, or a seen
// exit that is not yet delivered, in case the waiter's process dies first.
var holdGrace = 5 * time.Second

// Tools runs shell jobs for one Dyna session.
type Tools struct {
	Store *shells.Store
	// Exe is the dyna binary that supervises jobs.
	Exe          string
	Owner        string
	RoomID, Room string
	// Cwd is the session's working directory.
	Cwd   string
	Limit time.Duration
	// Foreground runs each command to its end. Outside a Dyna session no
	// session would hear when a background job ends, and Close stops what
	// is still running when the client goes away.
	Foreground bool
	// Env is extra environment for each command, as KEY=value pairs. A server
	// shared by many sessions sets it per call from Resolve.
	Env []string
	// WaitCap, when set, is the longest any call blocks. A client may drop a
	// connection whose call takes too long.
	WaitCap time.Duration
	// Resolve maps the client's own session ID to the Dyna session behind it.
	// A server shared by many sessions has no single owner. A call it cannot
	// map keeps this server's own owner, or foreground mode.
	Resolve func(clientSession string) (Caller, bool)
	// Search runs one web search in the engine, which holds every provider
	// credential. A session's own process never sees a token or a key.
	Search func(ctx context.Context, in callspec.WebSearchRequest) (callspec.WebSearchResponse, error)

	mu      sync.Mutex
	started []string
}

// Caller is the Dyna session behind one call.
type Caller struct {
	Owner, RoomID, Room, Cwd string
	Env                      []string
}

// forCaller returns the tools to run a call for the session the client names.
func (t *Tools) forCaller(clientSession string) *Tools {
	if clientSession == "" || t.Resolve == nil {
		return t
	}
	c, ok := t.Resolve(clientSession)
	if !ok {
		return t
	}
	return &Tools{Store: t.Store, Exe: t.Exe, Limit: t.Limit, WaitCap: t.WaitCap, Owner: c.Owner, RoomID: c.RoomID, Room: c.Room, Cwd: c.Cwd, Env: c.Env, Search: t.Search}
}

// capped is d, limited to the longest call may block.
func (t *Tools) capped(d time.Duration) time.Duration {
	if t.WaitCap > 0 && (d <= 0 || d > t.WaitCap) {
		return t.WaitCap
	}
	return d
}

// Close stops the jobs a foreground server started that still run.
func (t *Tools) Close() error {
	if !t.Foreground {
		return nil
	}
	t.mu.Lock()
	ids := t.started
	t.started = nil
	t.mu.Unlock()
	var errs []error
	for _, id := range ids {
		if in, err := t.Store.Info(id); err == nil && (in.Status == "running" || in.Status == "starting") {
			_, err = t.Store.Stop(id, "cleanup")
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// tools lists the tools as this server runs them.
func (t *Tools) tools() []toolDefinition {
	list := slices.Clone(toolList)
	shell := &list[0]
	if t.Foreground && t.Resolve == nil {
		shell.Description = "Run a bash command and wait for it to end. Each call starts a new shell in cwd, so cd does not carry over. " +
			"stdin is closed: use non-interactive flags. Output over 16 KB is cut in the middle, and the result names the full log file."
	} else {
		shell.Description = strings.Replace(shell.Description, "up to 30 seconds", "up to "+t.foregroundLimit().String(), 1)
	}
	if t.WaitCap > 0 {
		output := &list[1]
		output.Description += fmt.Sprintf(" A wait lasts at most %s, so call it again to keep waiting.", t.WaitCap)
	}
	return list
}

// foregroundLimit is how long a command runs before it moves to the background.
func (t *Tools) foregroundLimit() time.Duration {
	limit := t.Limit
	if limit <= 0 {
		limit = ForegroundLimit
	}
	return t.capped(limit)
}

// toolDefinition is one MCP tool as this server publishes it.
type toolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

// inputSchema is the object schema of a tool's arguments.
type inputSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]schemaProperty `json:"properties"`
	Required   []string                  `json:"required,omitempty"`
}

// schemaProperty is one argument of a tool: a JSON type and what it means.
type schemaProperty struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

var toolList = []toolDefinition{
	{
		Name: "shell",
		Description: "Run a bash command. It runs in the foreground for up to 30 seconds. " +
			"If it is still running then, it keeps running in the background and this returns its job ID. " +
			"Dyna wakes you with the exit code and last lines of output when it ends, so do not poll. " +
			"Each call starts a new shell in cwd, so cd does not carry over. stdin is closed: use non-interactive flags. " +
			"Output over 16 KB is cut in the middle, and the result names the full log file.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProperty{
				"command":     {Type: "string", Description: "The bash command to run."},
				"description": {Type: "string", Description: "One short line in active voice that says what the command does, for example \"Run the web tests\". Dyna shows it live."},
				"cwd":         {Type: "string", Description: "Working directory. Default: your session's working directory."},
			},
			Required: []string{"command", "description"},
		},
	},
	{
		Name: "shell_output",
		Description: "Read new output from a background job. With wait, block until the job ends (\"exit\") or until its new output matches a regular expression, " +
			"or until timeout_seconds passes. Without wait, return at once.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProperty{
				"job_id":          {Type: "string"},
				"wait":            {Type: "string", Description: "\"exit\", or a regular expression to wait for in new output."},
				"timeout_seconds": {Type: "integer", Description: "Longest wait, 1 to 600. Default 30."},
			},
			Required: []string{"job_id"},
		},
	},
	{
		Name:        "shell_stop",
		Description: "Stop a background job and its whole process group: SIGTERM, then SIGKILL after 5 seconds.",
		InputSchema: inputSchema{
			Type:       "object",
			Properties: map[string]schemaProperty{"job_id": {Type: "string"}},
			Required:   []string{"job_id"},
		},
	},
	{
		Name:        "shell_jobs",
		Description: "List your running jobs and the jobs that ended in the last hour.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProperty{}},
	},
	{
		Name: "web_search",
		Description: "Search the web. Returns titled results with a URL, a snippet and the provider that found them, " +
			"as compact JSON. The provider order is fixed: ChatGPT first when the owner signed in to it, then Brave.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProperty{
				"query": {Type: "string", Description: "What to search for."},
				"limit": {Type: "integer", Description: "How many results to return, 1 to 10. Default 5."},
			},
			Required: []string{"query"},
		},
	},
}

// Result is one tool call's answer and what the session learns from it.
// Nothing is marked seen until Commit, after the answer reaches the client.
type Result struct {
	Text   string
	Failed bool
	// claim is a finished job whose exit this answer reports.
	claim string
	// read is the job whose output this answer shows, up to offset.
	read   string
	offset int64
}

// Commit marks what the delivered answer showed as seen: the reported exit
// no longer wakes the session, and that output is not shown again.
func (t *Tools) Commit(r Result) error {
	if r.read != "" {
		if err := t.Store.SetReadOffset(r.read, r.offset); err != nil {
			return err
		}
	}
	if r.claim != "" {
		_, err := t.Store.ClaimExit(r.claim)
		return err
	}
	return nil
}

// Abort keeps an undelivered answer's exit eligible: the supervisor wakes
// the session at once rather than after the hold lapses.
func (t *Tools) Abort(r Result) error {
	if r.claim == "" {
		return nil
	}
	return t.Store.HoldExit(r.claim, time.Time{})
}

// Call runs one tool for the session the client names, if it names one.
// Commit or Abort the result.
func (t *Tools) Call(ctx context.Context, name string, raw json.RawMessage, clientSession string) Result {
	t = t.forCaller(clientSession)
	var args struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		Cwd         string `json:"cwd"`
		JobID       string `json:"job_id"`
		Wait        string `json:"wait"`
		Timeout     int    `json:"timeout_seconds"`
		Query       string `json:"query"`
		Limit       int    `json:"limit"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return Result{Text: "Invalid arguments: " + err.Error(), Failed: true}
		}
	}
	var r Result
	var err error
	switch name {
	case "shell":
		err = t.run(ctx, &r, args.Command, args.Description, args.Cwd)
	case "shell_output":
		err = t.output(ctx, &r, args.JobID, args.Wait, args.Timeout)
	case "shell_stop":
		err = t.stop(&r, args.JobID)
	case "shell_jobs":
		r.Text, err = t.jobs()
	case "web_search":
		r.Text, err = t.webSearch(ctx, args.Query, args.Limit)
	default:
		err = fmt.Errorf("unknown tool %q", name)
	}
	if err != nil {
		// A failed call reports no exit, so its hold must not delay the wake.
		_ = t.Abort(r)
		return Result{Text: err.Error(), Failed: true}
	}
	return r
}

func (t *Tools) run(ctx context.Context, r *Result, command, description, cwd string) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("command is required")
	}
	description = strings.TrimSpace(strings.SplitN(description, "\n", 2)[0])
	if description == "" {
		return errors.New("description is required: one short line that says what the command does")
	}
	if cwd == "" {
		cwd = t.Cwd
	} else if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(t.Cwd, cwd)
	}
	limit := t.foregroundLimit()
	if t.Foreground {
		limit = 0
	}
	cmd := []string{"bash", "-c", command}
	if len(t.Env) > 0 {
		cmd = append(append([]string{"env"}, t.Env...), cmd...)
	}
	meta, err := t.Store.Create(shells.StartOptions{Name: description, Cmd: cmd, Cwd: cwd, Owner: t.Owner, RoomID: t.RoomID, Room: t.Room})
	if err != nil {
		return err
	}
	// Hold the exit before the supervisor starts, so a fast command never
	// both returns here and wakes the session.
	if err := t.Store.HoldExit(meta.ID, holdUntil(limit)); err != nil {
		return err
	}
	if t.Foreground {
		t.mu.Lock()
		t.started = append(t.started, meta.ID)
		t.mu.Unlock()
	}
	if err := shells.Spawn(t.Exe, meta.ID); err != nil {
		return err
	}
	in, ended, err := t.wait(ctx, meta.ID, limit, nil)
	if ended {
		r.claim = meta.ID
	} else if err == nil {
		err = t.Store.MarkBackground(meta.ID, time.Now())
	}
	if err != nil {
		return err
	}
	out, err := t.newOutput(r, meta.ID)
	if err != nil {
		return err
	}
	if ended {
		r.Text = t.exitLine(in) + "\n" + out
		return nil
	}
	r.Text = fmt.Sprintf("Job %s. Still running after %s, so it moved to the background. "+
		"Dyna wakes you with its exit code and last lines when it ends. "+
		"Use shell_output to wait or read new output, or shell_stop to stop it.\n%s", meta.ID, limit, out)
	return nil
}

// wait blocks until the job ends, ready reports true, or the limit passes.
// It holds the exit while it waits. When the job ends, the hold stays until
// Commit claims the exit or Abort releases it.
func (t *Tools) wait(ctx context.Context, id string, limit time.Duration, ready func() bool) (shells.Info, bool, error) {
	if err := t.Store.HoldExit(id, holdUntil(limit)); err != nil {
		return shells.Info{}, false, err
	}
	in, _, err := t.Store.Wait(ctx, id, limit, ready)
	ended := in.Status == "exited" || in.Status == "lost"
	// A seen exit stays held only until Commit or Abort; if this process
	// dies first, the supervisor wakes the session after holdGrace.
	hold := time.Time{}
	if ended {
		hold = time.Now().Add(holdGrace)
	}
	if holdErr := t.Store.HoldExit(id, hold); err == nil {
		err = holdErr
	}
	return in, ended, err
}

// holdUntil is when a hold for a wait of limit lapses. 0 waits until the end.
func holdUntil(limit time.Duration) time.Time {
	if limit == 0 {
		limit = 24 * time.Hour
	}
	return time.Now().Add(limit + holdGrace)
}

func (t *Tools) output(ctx context.Context, r *Result, id, wait string, seconds int) error {
	in, err := t.own(id)
	if err != nil {
		return err
	}
	timeout := defaultWait
	if seconds > 0 {
		timeout = min(time.Duration(seconds)*time.Second, maxWait)
	}
	timeout = t.capped(timeout)
	matched := false
	switch {
	case wait == "":
	case wait == "exit":
		in, _, err = t.wait(ctx, id, timeout, nil)
	default:
		pattern, compileErr := regexp.Compile(wait)
		if compileErr != nil {
			return fmt.Errorf("wait is not \"exit\" or a valid regular expression: %w", compileErr)
		}
		from, offsetErr := t.Store.ReadOffset(id)
		if offsetErr != nil {
			return offsetErr
		}
		in, _, err = t.wait(ctx, id, timeout, func() bool {
			matched = t.matches(id, from, pattern)
			return matched
		})
	}
	if in.Status == "exited" || in.Status == "lost" {
		r.claim = id
	}
	if err != nil {
		return err
	}
	out, err := t.newOutput(r, id)
	if err != nil {
		return err
	}
	status := t.exitLine(in)
	switch {
	case matched:
		status = fmt.Sprintf("Job %s is running. Its output matched %q.", id, wait)
	case in.Status == "running" || in.Status == "starting":
		status = fmt.Sprintf("Job %s is still running.", id)
		if wait != "" {
			status = fmt.Sprintf("Job %s is still running after %s.", id, timeout)
		}
	}
	r.Text = status + "\n" + out
	return nil
}

func (t *Tools) stop(r *Result, id string) error {
	if _, err := t.own(id); err != nil {
		return err
	}
	// Stop takes up to 15 s. Hold the exit, so the session hears of it here.
	if err := t.Store.HoldExit(id, time.Now().Add(15*time.Second+holdGrace)); err != nil {
		return err
	}
	r.claim = id
	in, err := t.Store.Stop(id, "agent")
	if err != nil {
		return err
	}
	out, err := t.newOutput(r, id)
	if err != nil {
		return err
	}
	r.Text = t.exitLine(in) + "\n" + out
	return nil
}

func (t *Tools) jobs() (string, error) {
	list, err := t.Store.List(true)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, in := range list {
		if in.Owner != t.Owner || in.Exit != nil && time.Since(in.Exit.Ended) > time.Hour {
			continue
		}
		state := in.Status + " for " + time.Since(in.Started).Round(time.Second).String()
		if in.Exit != nil {
			state = fmt.Sprintf("exited with code %d %s ago", in.Exit.Code, time.Since(in.Exit.Ended).Round(time.Second))
		}
		fmt.Fprintf(&b, "%s  %s  %s\n", in.ID, state, in.Name)
	}
	if b.Len() == 0 {
		return "No jobs.", nil
	}
	return b.String(), nil
}

// webSearch runs one search in the engine and answers with the results as
// compact JSON. A provider failure becomes the tool's one-line error.
func (t *Tools) webSearch(ctx context.Context, query string, limit int) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("query is required")
	}
	if limit < 0 || limit > websearch.MaxLimit {
		return "", fmt.Errorf("limit must be from 1 to %d", websearch.MaxLimit)
	}
	if t.Search == nil {
		return "", errors.New("web search is not available in this session")
	}
	answer, err := t.Search(ctx, callspec.WebSearchRequest{Query: query, Limit: limit})
	if err != nil {
		return "", err
	}
	results := answer.Results
	if results == nil {
		results = []websearch.Result{}
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// own returns a job that belongs to this session.
func (t *Tools) own(id string) (shells.Info, error) {
	in, err := t.Store.Info(id)
	if err != nil {
		return shells.Info{}, err
	}
	if in.Owner != t.Owner {
		return shells.Info{}, fmt.Errorf("job %s belongs to another session", id)
	}
	return in, nil
}

// exitLine opens the answer of a call that ended a job. The "Job <id>." lead
// is how the transcript links the call to its job: keep it first.
func (t *Tools) exitLine(in shells.Info) string {
	switch {
	case in.Exit == nil:
		line := fmt.Sprintf("Job %s was lost: its supervisor ended without an exit code.", in.ID)
		if errs := t.Store.SupervisorErrors(in.ID); errs != "" {
			line += " Supervisor errors:\n" + errs
		}
		return line
	case in.Exit.Signal != "":
		return fmt.Sprintf("Job %s. Exit code %d (%s).", in.ID, in.Exit.Code, in.Exit.Signal)
	}
	return fmt.Sprintf("Job %s. Exit code %d.", in.ID, in.Exit.Code)
}

// matches reports whether output written after from matches pattern. It
// reads at most the last 64 KB.
func (t *Tools) matches(id string, from int64, pattern *regexp.Regexp) bool {
	path, err := t.Store.OutputPath(id)
	if err != nil {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false
	}
	from = max(from, st.Size()-64<<10)
	buf := make([]byte, st.Size()-from)
	n, _ := f.ReadAt(buf, from)
	return pattern.Match(buf[:n])
}

// newOutput returns output the session has not read yet, bounded to
// maxOutput. Commit moves the read cursor past it.
func (t *Tools) newOutput(r *Result, id string) (string, error) {
	path, err := t.Store.OutputPath(id)
	if err != nil {
		return "", err
	}
	from, err := t.Store.ReadOffset(id)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	text, err := bounded(f, from, st.Size(), path)
	if err != nil {
		return "", err
	}
	r.read, r.offset = id, st.Size()
	return text, nil
}

// bounded reads r[from:to]. Past maxOutput, it keeps the head and the tail
// and says how much it cut.
func bounded(r io.ReaderAt, from, to int64, path string) (string, error) {
	if to <= from {
		return "(no new output)", nil
	}
	read := func(off, n int64) (string, error) {
		buf := make([]byte, n)
		if _, err := r.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.ToValidUTF8(string(buf), ""), nil
	}
	if to-from <= maxOutput {
		return read(from, to-from)
	}
	head, err := read(from, outputHead)
	if err != nil {
		return "", err
	}
	tailSize := int64(maxOutput - outputHead)
	tail, err := read(to-tailSize, tailSize)
	if err != nil {
		return "", err
	}
	cut := to - from - maxOutput
	return fmt.Sprintf("%s\n[... %d bytes cut; full log: %s ...]\n%s", head, cut, path, tail), nil
}

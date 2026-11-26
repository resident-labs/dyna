// Package agy drives Google's Antigravity CLI over ACP (`agy acp`, JSON-RPC on stdio) as a harness. It is a port of how T3
// Code runs the agent: the same launch command, environment, lifecycle, permission answers, steer and cancel behaviour.
// What Dyna adds is the mapping to harness.Harness and a Pi-format mirror file of the conversation (mirror.go).
package agy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/agy/acp"
)

// MCPServer is a tool server given to agy. With a URL it is sent as an HTTP server (agy accepts one although it
// advertises no mcpCapabilities, as the spike found); otherwise Command, Args and Env make a stdio server.
type MCPServer struct {
	Name, URL string
	Headers   map[string]string
	Command   string
	Args      []string
	Env       map[string]string
}

// Config is what a session runs with.
type Config struct {
	Binary     string // /opt/agy/antigravity
	GeminiDir  string // --gemini-dir: the login, and with --app-data-dir agy's own data
	Dir        string // cwd of the process and the session
	SessionDir string // the mirror file, the agy session id
	Model      contract.ModelChoice
	// Instructions (Dyna's session instructions) and ProjectInstructions (one labelled entry per AGENTS.md) ride on the first
	// prompt, as T3 puts its instructions (T3 orchestrationInstructions.ts t3AcpPromptWithInstructions, adapter.ts:10022).
	// ModelInstructions are a context section, resent when TransferModel changes them.
	Instructions, ModelInstructions string
	ProjectInstructions             []string
	MCPServers                      []MCPServer
	New                             bool          // a fresh conversation instead of resuming the stored one
	CancelTimeout                   time.Duration // wait for the prompt's response after session/cancel, then kill; default 15s as T3 (ARUN:92)
	Notice                          func(string)  // out-of-turn warnings
}

// Harness drives one agy process and its conversation.
type Harness struct {
	// Activity hears what the turn is doing: each tool call.
	Activity func(contract.TurnID, contract.ActivityKind, string)
	// Finished hears each tool call as it ends.
	Finished func(contract.TurnID, contract.ToolFinished)

	cfg Config
	// The inactivity limits (turn.go); fields so a test can shorten them.
	promptInactivity, holdMax, watchEvery time.Duration
	done                                  chan struct{}
	doneOnce                              sync.Once
	starting                              sync.Mutex // serializes spawning a process

	fm      sync.Mutex // the mirror file: its path and entry count; never held with mu
	mirror  string
	nonce   int64
	entries int64

	mu        sync.Mutex
	p         *process
	sessionID string
	turn      *turn
	held      []contract.TurnInput // follow-ups waiting for the prompt response
	sections  []contract.ContextSection
	told      told            // what the process was told; it starts empty with each process (T3 adapter.ts promptInstructionStates)
	broken    bool            // agy's saved conversation fails every prompt (INVALID_THINKING_SIGNATURE); the next Start opens a new one
	detached  bool            // a turn ended with agy still running a tool call or a reply: Abort then cancels it (quirk 3)
	restart   bool            // a prompt ended in a failure found in the session log: the next use starts a new process (T3 runtimeRestartRequired)
	blocked   bool            // the last turn ended in a limit or an account refusal: Compact sends no prompt (root ruling 5)
	idle      *turn           // what agy did with no turn running, until it is written to the mirror
	compacted map[string]bool // compactions already seen, by id
	modelText string
	context   int64 // input and cached tokens of the last prompt
	used      int64 // agy's usage_update
	commands  map[string]bool
	closed    bool

	delivered deliveries
}

var _ harness.Harness = (*Harness)(nil)

// process is one running `agy acp`.
type process struct {
	cmd     *exec.Cmd
	client  *acp.Client
	stdin   io.Closer
	exited  chan struct{}
	version string
	options []configOption
	tail    tail

	resume                 bool // sessionCapabilities.resume
	image, audio, embedded bool // promptCapabilities
}

// configOption is a session config option. A choice is a value, or a group of choices (T3 ACPS:253-334 reads both).
type configOption struct {
	ID           string `json:"id"`
	CurrentValue string `json:"currentValue"`
	Options      []struct {
		Value   string `json:"value"`
		Options []struct {
			Value string `json:"value"`
		} `json:"options"`
	} `json:"options"`
}

func (o configOption) lists(value string) bool {
	for _, c := range o.Options {
		if c.Value == value || slices.ContainsFunc(c.Options, func(g struct {
			Value string `json:"value"`
		}) bool {
			return g.Value == value
		}) {
			return true
		}
	}
	return false
}

// tail keeps the end of the process's stderr for error messages.
type tail struct {
	mu   sync.Mutex
	text string
}

func (t *tail) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.text = (t.text + line + "\n")
	if len(t.text) > 2048 {
		t.text = t.text[len(t.text)-2048:]
	}
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return harness.Mask(strings.ToValidUTF8(t.text, ""))
}

// Open starts agy, signs in and opens the conversation: the stored one unless Config.New.
func Open(ctx context.Context, c Config) (*Harness, error) {
	if c.Binary == "" || c.GeminiDir == "" || c.Dir == "" || c.SessionDir == "" {
		return nil, errors.New("agy needs Binary, GeminiDir, Dir and SessionDir")
	}
	if c.CancelTimeout == 0 {
		c.CancelTimeout = 15 * time.Second
	}
	if err := os.MkdirAll(c.SessionDir, 0o700); err != nil {
		return nil, err
	}
	meta, err := readMeta(c.SessionDir)
	if err != nil {
		return nil, err
	}
	h := &Harness{cfg: c, done: make(chan struct{}), modelText: c.ModelInstructions, nonce: time.Now().UnixNano(),
		promptInactivity: defaultPromptInactivity, holdMax: defaultHoldMax, watchEvery: defaultWatchEvery}
	if !c.New {
		h.sessionID, h.mirror = meta.SessionID, filepath.Join(c.SessionDir, meta.Mirror)
	}
	if h.sessionID == "" || meta.Mirror == "" {
		h.sessionID = ""
		if h.mirror, err = nextMirror(c.SessionDir); err != nil {
			return nil, err
		}
	}
	if err := h.ensure(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

// dropped is the ambient environment T3 removes so nothing but the profile decides the account (ASUP:62-82).
var dropped = []string{
	"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION",
	"GOOGLE_CLOUD_QUOTA_PROJECT", "GOOGLE_GENAI_USE_VERTEXAI", "GCLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT", "AGY_ACP_CCPA_PROJECT",
	"AGY_ACP_ENABLE_OAUTH", "GEMINI_HOME", "AGY_ACP_FORCE_FILE_STORAGE", "AGY_CLI_DISABLE_AUTO_UPDATE", "BROWSER",
	"PYTHONUNBUFFERED", "ELECTRON_RUN_AS_NODE", "CHROME_CRASHPAD_PIPE_NAME", "TMPDIR",
}

var caBundles = []string{"/etc/ssl/certs/ca-bundle.crt", "/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"}

// browserCommand is BROWSER: a command that opens nothing and exits 0, as T3's helper does (ASUP:56-60, 198-200, 239): agy must
// never start a browser, and an exit of 0 keeps Python's webbrowser from trying another. T3's helper also prints the URL, which
// Dyna has no sign-in screen for. The command has no colon, which Python would split on (ASUP:430-431).
const browserCommand = `'/bin/sh' '-c' 'exit 0' '--' '%s'`

// environment is T3's agent environment (ASUP:202-246). HOME and PATH come from base. T3's Electron-as-node variable is left
// out, as there is no helper to run with it. SSH_CONNECTION is the host's, else a fake one (ASUP:230-234), so the
// 1.5 CLI keeps the login in the profile and not in the OS keyring.
func environment(base []string, geminiDir, tmp string) []string {
	var env []string
	hasCA, hasSSH, nix := false, false, ""
	for _, kv := range base {
		name, value, _ := strings.Cut(kv, "=")
		// T3 sets the CA file only when neither variable is defined at all (ASUP:225-229), and the host's SSH_CONNECTION when non-empty.
		hasCA = hasCA || name == "SSL_CERT_FILE" || name == "SSL_CERT_DIR"
		hasSSH = hasSSH || (name == "SSH_CONNECTION" && value != "")
		if name == "NIX_SSL_CERT_FILE" {
			nix = value
		}
		// T3 matches the names case-insensitively (ASUP:204-209).
		if !slices.Contains(dropped, strings.ToUpper(name)) {
			env = append(env, kv)
		}
	}
	env = append(env, "GEMINI_HOME="+geminiDir, "AGY_ACP_FORCE_FILE_STORAGE=1", "AGY_CLI_DISABLE_AUTO_UPDATE=true",
		"BROWSER="+browserCommand, "PYTHONUNBUFFERED=1", "TMPDIR="+tmp)
	if !hasSSH {
		env = append(env, "SSH_CONNECTION=127.0.0.1 0 127.0.0.1 0")
	}
	if !hasCA {
		// Relative paths would resolve differently in the project directory (ASUP:443-469).
		for _, path := range append([]string{nix}, caBundles...) {
			if !filepath.IsAbs(path) {
				continue
			}
			if _, err := os.Stat(path); err == nil {
				env = append(env, "SSL_CERT_FILE="+path)
				break
			}
		}
	}
	return env
}

// parseVersion reads agy's version as T3 does, tolerating the historical agy_acp_server_ prefix (ASUP:62-66, quirk 32).
var versionPattern = regexp.MustCompile(`^(?:v|agy_acp_server_)?(\d+)\.(\d+)\.(\d+)`)

// minVersion is the first agy that offers every model to every ACP client. T3 refuses older ones (ASUP:70-88, AntigravityDriver.ts:
// 340-356) and its model manifest marks below 1.5.0 broken; there is no withdrawn build listed. The steering that 1.5.0 added
// (ASUP:77-85) is therefore always there. An agent that gives no parseable version is not refused, as in T3.
var minVersion = [3]int{1, 5, 1}

func versionRefused(version string) error {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return nil
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	if slices.Compare(v[:], minVersion[:]) < 0 {
		return fmt.Errorf("agy %s only serves allowlisted editors, not Dyna; use 1.5.1 or newer", version)
	}
	return nil
}

// ensure runs a live process with the conversation open on it, starting one if the last has gone.
func (h *Harness) ensure(ctx context.Context) error {
	h.starting.Lock()
	defer h.starting.Unlock()
	h.mu.Lock()
	p, closed, restart := h.p, h.closed, h.restart
	h.restart = false
	h.mu.Unlock()
	if closed {
		return errors.New("agy harness is closed")
	}
	if p != nil && restart {
		// T3 restarts the runtime before the turn that follows a failure it read from the session log (adapter.ts:10077-10094,
		// 10280), so a prompt agy dropped is not assumed delivered.
		p.kill()
	} else if p != nil {
		// A connection that has closed is a process that is going: start another, without waiting for it to be reaped.
		select {
		case <-p.exited:
		case <-p.client.Done():
		default:
			return nil
		}
	}
	p, err := h.spawn()
	if err == nil {
		err = h.setup(ctx, p)
	}
	if err != nil {
		if p != nil {
			p.kill()
		}
		return err
	}
	h.mu.Lock()
	h.p = p
	// What a process was told, and any work it ran, goes with it (T3 keeps this state per runtime).
	h.resetTold()
	h.detached = false
	h.mu.Unlock()
	return nil
}

func (h *Harness) spawn() (p *process, err error) {
	tmp, err := os.MkdirTemp("", "agy-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmp)
		}
	}()
	cmd := exec.Command(h.cfg.Binary, "acp", "--gemini-dir", h.cfg.GeminiDir, "--app-data-dir", "antigravity-acp")
	cmd.Dir = h.cfg.Dir
	cmd.Env = environment(os.Environ(), h.cfg.GeminiDir, tmp)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start agy: %w", err)
	}
	p = &process{cmd: cmd, stdin: stdin, exited: make(chan struct{})}
	p.client = acp.NewClient(stdout, stdin, acp.Handler{Request: h.request, Notify: h.notify})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		reader := bufio.NewReaderSize(stderr, 16<<10)
		for {
			line, err := acp.ReadLine(reader, 16<<10)
			if err != nil {
				return
			}
			p.tail.add(string(line))
		}
	}()
	go func() {
		<-p.client.Done()
		<-drained
		cmd.Wait()
		os.RemoveAll(tmp)
		close(p.exited)
	}()
	return p, nil
}

// kill ends the process and anything it started, and waits for it to be gone so the next turn starts a new one.
func (p *process) kill() {
	syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-p.exited:
	case <-time.After(2 * time.Second):
	}
}

// setup runs the lifecycle (ARUN:2326-2530, ACPS:115-155): initialize, authenticate, session resume or new. The sign-in error test is ASUP:167-177.
func (h *Harness) setup(ctx context.Context, p *process) error {
	var init struct {
		AgentInfo struct{ Version string } `json:"agentInfo"`
		Caps      struct {
			Prompt struct {
				Image           bool `json:"image"`
				Audio           bool `json:"audio"`
				EmbeddedContext bool `json:"embeddedContext"`
			} `json:"promptCapabilities"`
			Session struct {
				Resume *json.RawMessage `json:"resume"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	err := p.client.Call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientInfo":         map[string]any{"name": "dyna", "title": "Dyna", "version": "0.1"},
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
	}, &init)
	if err != nil {
		return h.exitError("initialize", err, p)
	}
	p.version = init.AgentInfo.Version
	if err := versionRefused(p.version); err != nil {
		return err
	}
	p.image, p.audio, p.embedded = init.Caps.Prompt.Image, init.Caps.Prompt.Audio, init.Caps.Prompt.EmbeddedContext
	p.resume = init.Caps.Session.Resume != nil
	if err := h.authenticate(ctx, p); err != nil {
		return err
	}
	err = h.openSession(ctx, p, h.sessionID)
	// T3 authenticates again and retries the session setup once when it fails with auth-required, -32000 (ARUN:2411-2455, 2525-2531).
	if errors.Is(err, ErrSignInRequired) {
		if err = h.authenticate(ctx, p); err == nil {
			err = h.openSession(ctx, p, h.sessionID)
		}
	}
	return err
}

// authenticate signs in with oauth-personal, the only method Dyna uses. An agent with no sign-in is ASUP:167-177.
func (h *Harness) authenticate(ctx context.Context, p *process) error {
	err := p.client.Call(ctx, "authenticate", map[string]any{"methodId": "oauth-personal"}, nil)
	var rpc *acp.Error
	if errors.As(err, &rpc) && strings.Contains(rpc.Message, "SUBSCRIPTION_REQUIRED") {
		return ErrSubscriptionRequired
	}
	if errors.As(err, &rpc) && (rpc.Code == -32000 || (rpc.Code == -32603 && strings.HasPrefix(rpc.Message, "No browser is available here"))) {
		return ErrSignInRequired
	}
	if err != nil {
		return h.exitError("authenticate", err, p)
	}
	return nil
}

// exitError is what a failed call shows. T3 never shows agy's own text for a failed call or an exit: a process exit is "provider
// process exited" with only its status, an auth-required error a fixed sign-in message, and any other error a category (provider-core
// failure.ts:curatedCauseMessages, AntigravityAuth.ts:safeAuthFailure). What agy said goes to the box log instead.
func (h *Harness) exitError(what string, err error, p *process) error {
	if errors.Is(err, acp.ErrClosed) {
		log.Printf("agy exited during %s: %s", what, p.tail.String())
		return fmt.Errorf("agy exited unexpectedly during %s; its output is in the box log", what)
	}
	var rpc *acp.Error
	if !errors.As(err, &rpc) {
		log.Printf("agy %s: %v", what, err)
		return fmt.Errorf("agy %s failed", what)
	}
	log.Printf("agy %s: %v", what, err)
	switch {
	case rpc.Code == -32000:
		return ErrSignInRequired
	case what == "session/new" && rpc.Code == -32603:
		return errors.New("agy authenticated, but could not start a session or load models")
	}
	return fmt.Errorf("agy %s failed (code %d); its message is in the box log", what, rpc.Code)
}

// current is every context section the session should be told, the model's guidance first.
func (h *Harness) current() []contract.ContextSection {
	var all []contract.ContextSection
	if h.modelText != "" {
		all = append(all, contract.ContextSection{Key: "model_instructions", Text: h.modelText})
	}
	return append(all, h.sections...)
}

func (h *Harness) mcpServers() []map[string]any {
	servers := []map[string]any{}
	pairs := func(m map[string]string) []map[string]string {
		list := []map[string]string{}
		for _, name := range slices.Sorted(func(yield func(string) bool) {
			for k := range m {
				if !yield(k) {
					return
				}
			}
		}) {
			list = append(list, map[string]string{"name": name, "value": m[name]})
		}
		return list
	}
	for _, s := range h.cfg.MCPServers {
		if s.URL != "" {
			servers = append(servers, map[string]any{"type": "http", "name": s.Name, "url": s.URL, "headers": pairs(s.Headers)})
		} else {
			servers = append(servers, map[string]any{"name": s.Name, "command": s.Command, "args": append([]string{}, s.Args...), "env": pairs(s.Env)})
		}
	}
	return servers
}

// openSession resumes the conversation, or starts one when there is none, and applies the model. agy keeps its
// conversations under --gemini-dir/antigravity-acp (--app-data-dir is joined under --gemini-dir), so they live with the login
// and not in SessionDir. Nothing is appended to agy's system prompt: T3 appends only a session-invariant block
// (ASUP:87-105), and Dyna has none. The instructions ride on the first prompt (turn.go).
func (h *Harness) openSession(ctx context.Context, p *process, resume string) error {
	params := map[string]any{"cwd": h.cfg.Dir, "mcpServers": h.mcpServers()}
	var res struct {
		SessionID     string         `json:"sessionId"`
		ConfigOptions []configOption `json:"configOptions"`
		Modes         struct {
			Current string `json:"currentModeId"`
		} `json:"modes"`
	}
	// T3 has no fallback: a conversation agy cannot resume fails the open (ARUN:2463-2490), and so does an agent that does not
	// advertise resume.
	id := resume
	if resume != "" {
		if !p.resume {
			return errors.New("agy does not advertise session/resume, so its conversation cannot be resumed")
		}
		params["sessionId"] = resume
		if err := p.client.Call(ctx, "session/resume", params, &res); err != nil {
			return h.exitError("session/resume", err, p)
		}
	} else {
		if err := p.client.Call(ctx, "session/new", params, &res); err != nil {
			return h.exitError("session/new", err, p)
		}
		id = res.SessionID
	}
	p.options = res.ConfigOptions
	// A new conversation gets a new mirror file.
	mirror := h.mirrorPath()
	if resume == "" {
		var err error
		if mirror, err = nextMirror(h.cfg.SessionDir); err != nil {
			return err
		}
	}
	h.mu.Lock()
	h.sessionID = id
	h.mu.Unlock()
	h.fm.Lock()
	h.mirror = mirror
	h.fm.Unlock()
	if err := writeMeta(h.cfg.SessionDir, sessionMeta{SessionID: id, Mirror: filepath.Base(mirror)}); err != nil {
		return err
	}
	// Dyna answers permissions itself, as T3's full-access mode does, which keeps agy in `default` (T3 antigravitySessionMode,
	// ASUP:170-185: yolo and auto_edit stay default); a session that resumed in another mode is put back.
	if res.Modes.Current != "" && res.Modes.Current != "default" {
		if err := p.client.Call(ctx, "session/set_mode", map[string]any{"sessionId": id, "modeId": "default"}, nil); err != nil {
			return h.exitError("session/set_mode", err, p)
		}
	}
	return h.applyModel(ctx, p)
}

func (h *Harness) notice(text string) {
	if h.cfg.Notice != nil {
		h.cfg.Notice(text)
	} else {
		log.Print(text)
	}
}

// applyModel sets the configured model and thinking level; agy's cold resume reports its default (quirk 26), so it runs
// after every new and resume. A value agy does not list is the agent's refusal.
func (h *Harness) applyModel(ctx context.Context, p *process) error {
	h.mu.Lock()
	model, sid := h.cfg.Model, h.sessionID
	h.mu.Unlock()
	for _, set := range []struct{ id, value string }{{"model", model.ID}, {"thought_level", string(model.Thinking)}} {
		if set.value == "" {
			continue
		}
		listed := slices.ContainsFunc(p.options, func(o configOption) bool { return o.ID == set.id && o.lists(set.value) })
		if !listed {
			return fmt.Errorf("%w: agy does not list %s %q", harness.ErrRefused, set.id, set.value)
		}
		var res struct {
			ConfigOptions []configOption `json:"configOptions"`
		}
		err := p.client.Call(ctx, "session/set_config_option", map[string]any{"sessionId": sid, "configId": set.id, "value": set.value}, &res)
		var rpc *acp.Error
		if errors.As(err, &rpc) {
			return fmt.Errorf("%w: %v", harness.ErrRefused, rpc)
		}
		if err != nil {
			return h.exitError("session/set_config_option", err, p)
		}
		if res.ConfigOptions != nil {
			p.options = res.ConfigOptions
		}
	}
	return nil
}

// Done closes when the harness is closed, or when agy exited and could not be started again.
func (h *Harness) Done() <-chan struct{} { return h.done }

// SessionFile is the mirror file.
func (h *Harness) SessionFile() string { return h.mirrorPath() }

func (h *Harness) mirrorPath() string {
	h.fm.Lock()
	defer h.fm.Unlock()
	return h.mirror
}

// Close closes agy's stdin, gives it two seconds, then kills its process group.
func (h *Harness) Close() error {
	h.mu.Lock()
	h.closed = true
	p := h.p
	h.mu.Unlock()
	if p != nil {
		p.stdin.Close()
		select {
		case <-p.exited:
		case <-time.After(2 * time.Second):
			p.kill()
			<-p.exited
		}
	}
	h.doneOnce.Do(func() { close(h.done) })
	return nil
}

// Package agy drives Google's Antigravity CLI over ACP (`agy acp`, JSON-RPC on stdio) as a harness. It is a port of how T3
// Code runs the agent: the same launch command, environment, lifecycle, permission answers, steer and cancel behaviour.
// What Dyna adds is the mapping to harness.Harness and a Pi-format mirror file of the conversation (mirror.go).
package agy

import (
	"bufio"
	"context"
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
	// Instructions and ModelInstructions are appended to agy's system prompt at session new and resume.
	Instructions, ModelInstructions string
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
	promptInactivity, toolInactivity, watchEvery time.Duration
	done                                         chan struct{}
	doneOnce                                     sync.Once
	starting                                     sync.Mutex // serializes spawning a process

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
	sent      []contract.ContextSection // what the session was told: the append, then each prompt's diff
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
	steers  bool
	append  bool
	options []configOption
	tail    tail
}

type configOption struct {
	ID           string `json:"id"`
	CurrentValue string `json:"currentValue"`
	Options      []struct {
		Value string `json:"value"`
	} `json:"options"`
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
		promptInactivity: defaultPromptInactivity, toolInactivity: defaultToolInactivity, watchEvery: defaultWatchEvery}
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

// environment is T3's agent environment (ASUP:202-246). HOME and PATH come from base. T3's BROWSER helper and Electron
// variable are left out: Dyna signs in outside the harness. SSH_CONNECTION is the host's, else a fake one (ASUP:230-234), so the
// 1.5 CLI keeps the login in the profile and not in the OS keyring.
func environment(base []string, geminiDir, tmp string) []string {
	var env []string
	hasCA, hasSSH := false, false
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if name == "SSL_CERT_FILE" && kv != "SSL_CERT_FILE=" {
			hasCA = true
		}
		hasSSH = hasSSH || (name == "SSH_CONNECTION" && kv != "SSH_CONNECTION=")
		if !slices.Contains(dropped, name) {
			env = append(env, kv)
		}
	}
	env = append(env, "GEMINI_HOME="+geminiDir, "AGY_ACP_FORCE_FILE_STORAGE=1", "AGY_CLI_DISABLE_AUTO_UPDATE=true",
		"PYTHONUNBUFFERED=1", "TMPDIR="+tmp)
	if !hasSSH {
		env = append(env, "SSH_CONNECTION=127.0.0.1 0 127.0.0.1 0")
	}
	if !hasCA {
		for _, path := range caBundles {
			if _, err := os.Stat(path); err == nil {
				env = append(env, "SSL_CERT_FILE="+path)
				break
			}
		}
	}
	return env
}

var versionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// steersWithPrompt is T3's check: agy 1.5.0 and later steers a running turn with the next prompt.
func steersWithPrompt(version string) bool {
	m := versionPattern.FindStringSubmatch(version)
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > 1 || (major == 1 && minor >= 5)
}

// ensure runs a live process with the conversation open on it, starting one if the last has gone.
func (h *Harness) ensure(ctx context.Context) error {
	h.starting.Lock()
	defer h.starting.Unlock()
	h.mu.Lock()
	p, closed := h.p, h.closed
	h.mu.Unlock()
	if closed {
		return errors.New("agy harness is closed")
	}
	if p != nil {
		select {
		case <-p.exited:
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
			Meta struct {
				Antigravity struct {
					SystemPrompt struct{ Append bool } `json:"systemPrompt"`
				}
			} `json:"_meta"`
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
	p.steers = steersWithPrompt(p.version)
	p.append = init.Caps.Meta.Antigravity.SystemPrompt.Append
	if err := h.authenticate(ctx, p); err != nil {
		return err
	}
	err = h.openSession(ctx, p, h.sessionID)
	// T3 authenticates again and retries the session setup once when it fails with auth-required, -32000 (ARUN:2411-2455, 2525-2531).
	var rpc *acp.Error
	if errors.As(err, &rpc) && rpc.Code == -32000 {
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

// exitError adds what agy said on stderr when the process ended during a call.
func (h *Harness) exitError(what string, err error, p *process) error {
	if errors.Is(err, acp.ErrClosed) {
		return fmt.Errorf("agy exited during %s: %s", what, p.tail.String())
	}
	return fmt.Errorf("agy %s: %w", what, err)
}

// current is every context section the session should know, the model's guidance first.
func (h *Harness) current() []contract.ContextSection {
	var all []contract.ContextSection
	if h.modelText != "" {
		all = append(all, contract.ContextSection{Key: "model_instructions", Text: h.modelText})
	}
	return append(all, h.sections...)
}

// appendText is the system prompt agy gets: Dyna's instructions and every section, as pig puts them in its system prompt.
func (h *Harness) appendText() string {
	parts := []string{h.cfg.Instructions}
	for _, s := range h.current() {
		parts = append(parts, "<"+string(s.Key)+">\n"+s.Text+"\n</"+string(s.Key)+">")
	}
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), "\n\n")
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

// openSession resumes the conversation, or starts one when there is none or agy no longer has it, and applies the model.
// agy keeps its conversations under --gemini-dir/antigravity-acp (--app-data-dir is joined under --gemini-dir), so they
// live with the login and not in SessionDir. TODO: spike A decides whether the box can split them.
func (h *Harness) openSession(ctx context.Context, p *process, resume string) error {
	params := map[string]any{"cwd": h.cfg.Dir, "mcpServers": h.mcpServers()}
	h.mu.Lock()
	text := h.appendText()
	h.mu.Unlock()
	if p.append && text != "" {
		params["_meta"] = map[string]any{"antigravity": map[string]any{"systemPrompt": map[string]any{"append": text}}}
	}
	var res struct {
		SessionID     string         `json:"sessionId"`
		ConfigOptions []configOption `json:"configOptions"`
	}
	// T3 has no fallback: a conversation agy cannot resume fails the open (ARUN:2463-2490).
	id := resume
	if resume != "" {
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
	h.sessionID, h.sent = id, h.current()
	h.mu.Unlock()
	h.fm.Lock()
	h.mirror = mirror
	h.fm.Unlock()
	if err := writeMeta(h.cfg.SessionDir, sessionMeta{SessionID: id, Mirror: filepath.Base(mirror)}); err != nil {
		return err
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
		listed := false
		for _, option := range p.options {
			listed = listed || (option.ID == set.id && slices.ContainsFunc(option.Options, func(o struct {
				Value string `json:"value"`
			}) bool {
				return o.Value == set.value
			}))
		}
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

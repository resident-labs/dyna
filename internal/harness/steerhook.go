package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/fsutil"
	harnesshome "dyna-agent/internal/harness/home"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/shellquote"
)

// Hook-based steering. Codex and Muse run a PostToolUse hook given on
// their command line (-c) and add the hook's additionalContext to the running
// turn before the next model call. For those harnesses dyna installs its own
// hook for each worker run: a pending steering message goes into a per-run
// handoff directory, and the hook (`dyna hook steer --dir <dir>`) hands it to the model
// at the next tool boundary. The worker is never stopped or resumed for it.
//
// If the hook does not take the message before the turn ends, the message is
// the next continuation. If a tool call completes and the hook still has not
// taken it after steerHookGrace, dyna counts the hook as failed for this
// attempt and falls back to the interrupt-between-tool-calls path.

// EnvSteerHooks set to "off" turns hook delivery off; steering then always
// uses the interrupt-between-tool-calls path.
const EnvSteerHooks = "DYNA_STEER_HOOKS"

const (
	steerHookTimeout = 10 // seconds the harness gives the hook
)

// steerHookGrace is how long an offered message may stay untaken after a tool
// call completed before dyna stops trusting the hook.
var steerHookGrace = 20 * time.Second

// museHookMinVersion is the oldest Muse release whose `muse exec -c hooks=`
// is known to run PostToolUse hooks headless.
var museHookMinVersion = [3]int{1, 4, 7}

// museMemoryDenyHook denies Muse's native memory tools in the managed hooks
// file. Dyna sessions keep no native memory. The command is a fixed printf,
// so no script file is needed.
var museMemoryDenyHook = map[string]any{
	"matcher": "read_memory|add_memory|edit_memory",
	"hooks":   []any{map[string]any{"type": "command", "command": `printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Dyna sessions keep no native memory; use your goal, the room or files you own."}}'`}},
}

// steerHookSupport reports whether the CLI at executable, Dyna's private copy
// of harness h, runs dyna's command hooks. Tests replace it.
var steerHookSupport = probeSteerHookSupport

// steerHookCommand is the shell command line the harness runs as the hook.
// Tests replace it.
var steerHookCommand = func(dir string) (string, error) {
	exe, err := fsutil.RunningExecutable()
	if err != nil {
		return "", err
	}
	return shellquote.Quote(exe) + " hook steer --dir " + shellquote.Quote(dir), nil
}

// steerHook is one worker run's hook handoff directory. Only the run's event loop uses it.
type steerHook struct {
	harness string
	dir     string
	command string
	// steer: the PostToolUse steering hook runs. Muse's memory denial does not depend on it.
	steer bool
	// failed: the hook did not take a message in time during this attempt;
	// later messages use the interrupt path until the next attempt.
	failed      bool
	outstanding []runstore.SteeringMessage
	transport   *hookReceiptTransport
	offeredAt   time.Time
	boundaryAt  time.Time // first tool completion after the offer
}

// newSteerHook returns nil when the harness has no usable command hook. Muse
// always gets one, for its memory denial; the other harnesses need steering.
// Codex and Muse own hooks for the process lifetime, rather than one turn.
func newSteerHookForTurn(p profile.Profile) (*steerHook, error) {
	if p.Harness == profile.HarnessCodex || p.Harness == profile.HarnessMuse {
		return nil, nil
	}
	return newSteerHook(p)
}

func newSteerHook(p profile.Profile) (*steerHook, error) {
	if !steerHookSupport(p.Harness, p.RuntimeExecutable) {
		return nil, nil
	}
	steer := steerHooksOn()
	if !steer && p.Harness != profile.HarnessMuse {
		return nil, nil
	}
	dir, err := os.MkdirTemp("", "dyna-steer-*")
	if err != nil {
		return nil, err
	}
	command, err := steerHookCommand(dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return &steerHook{harness: p.Harness, dir: dir, command: command, steer: steer}, nil
}

func (h *steerHook) cleanup() {
	if h != nil {
		if h.transport != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, _, _ = h.transport.retire(ctx)
			cancel()
		}
		os.RemoveAll(h.dir)
	}
}

// usable reports whether steering should go through the hook now.
func (h *steerHook) usable() bool { return h != nil && h.steer && !h.failed }

// argv adds the hook to a harness command line, right after its subcommands.
func (h *steerHook) argv(argv, launchEnv []string) ([]string, error) {
	if h == nil {
		return argv, nil
	}
	var extra []string
	switch h.harness {
	case profile.HarnessCodex:
		// The trust bypass is needed for a hook given with -c to run at all.
		// It also lifts the review step for any other hook enabled for this
		// invocation (user or project config); see `dyna guide steering`.
		extra = []string{"-c", fmt.Sprintf(`hooks.PostToolUse=[{matcher="*",hooks=[{type="command",command=%s,timeout=%d}]}]`, tomlString(h.command), steerHookTimeout),
			"--dangerously-bypass-hook-trust"}
	case profile.HarnessMuse:
		hooks := map[string]any{"PreToolUse": []any{museMemoryDenyHook}}
		if h.steer {
			hooks["PostToolUse"] = []any{map[string]any{"matcher": "*", "hooks": []any{map[string]any{"type": "command", "command": h.command}}}}
		}
		// Muse clears user-hook environments. A managed hook can request names
		// from the final launch without putting values in argv. Limit the grant to
		// Dyna identity and private roots; Muse rejects provider credential names.
		names := []string{}
		for _, name := range harnessruntime.DynaShellEnvNames(launchEnv) {
			if harnesshome.PrivateEnvironmentKey(name) || strings.HasPrefix(name, "DYNA_") {
				names = append(names, name)
			}
		}
		path := filepath.Join(h.dir, "hooks.json")
		data, err := json.Marshal(map[string]any{"hooks": hooks})
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			return nil, err
		}
		encodedPath, _ := json.Marshal(path)
		encodedNames, _ := json.Marshal(names)
		extra = []string{"-c", "hooks={}", "-c", "managed_hooks_path=" + string(encodedPath),
			"-c", "managed_hooks_env_vars=" + string(encodedNames)}
	default:
		return argv, nil
	}
	at := len(argv)
	for i := 1; i < len(argv); i++ {
		if strings.HasPrefix(argv[i], "-") {
			at = i
			break
		}
	}
	out := append([]string(nil), argv[:at]...)
	out = append(out, extra...)
	return append(out, argv[at:]...), nil
}

// offer reserves messages for the hook of the given session. since is when the
// oldest of them was first offered.
func (h *steerHook) offer(session string, messages []runstore.SteeringMessage, since time.Time) error {
	if h.transport == nil {
		t, err := startHookReceiptTransport(h.dir)
		if err != nil {
			return err
		}
		h.transport = t
	}
	if err := h.transport.offer(session, messages); err != nil {
		return err
	}
	h.outstanding, h.offeredAt, h.boundaryAt = messages, since, time.Time{}
	return nil
}

// taken returns the outstanding messages once the hook proved a complete write.
func (h *steerHook) taken() []runstore.SteeringMessage {
	if h == nil || h.transport == nil || len(h.outstanding) == 0 {
		return nil
	}
	messages := h.transport.taken()
	if len(messages) > 0 {
		h.outstanding = nil
	}
	return messages
}

// withdraw takes back the outstanding messages the hook has not been granted. It
// returns those taken back and those the hook delivered meanwhile. Messages a
// hook process may still be writing stay reserved until retire resolves them.
func (h *steerHook) withdraw() (back, delivered []runstore.SteeringMessage) {
	if h == nil || h.transport == nil || len(h.outstanding) == 0 {
		return nil, nil
	}
	back, delivered = h.transport.withdraw()
	if len(back) > 0 || len(delivered) > 0 {
		h.outstanding = nil
	}
	return back, delivered
}

// retire stops the hook transport after the harness stopped. It joins or kills a
// granted hook process first. On error nothing is returned for replay, because
// the hook may have delivered it. A later offer starts a new transport.
func (h *steerHook) retire(ctx context.Context) (back, delivered []runstore.SteeringMessage, err error) {
	if h == nil || h.transport == nil {
		return nil, nil, nil
	}
	back, delivered, err = h.transport.retire(ctx)
	if err != nil {
		return nil, nil, err
	}
	h.transport, h.outstanding = nil, nil
	return back, delivered, nil
}

// noteBoundary records a tool completion while a message is outstanding.
func (h *steerHook) noteBoundary() {
	if h != nil && len(h.outstanding) > 0 && h.boundaryAt.IsZero() {
		h.boundaryAt = time.Now()
	}
}

// stale reports an offer that a completed tool call's hook should have taken.
func (h *steerHook) stale() bool {
	return h != nil && len(h.outstanding) > 0 && !h.boundaryAt.IsZero() && !h.transport.reserved() && time.Since(h.boundaryAt) >= steerHookGrace
}

// RunSteerHook delivers pending steering as additionalContext after tool calls.
func RunSteerHook(dir string, in io.Reader, out io.Writer) error {
	var event struct {
		SessionID     string `json:"session_id"`
		HookEventName string `json:"hook_event_name"`
	}
	b, _ := io.ReadAll(io.LimitReader(in, 4<<20))
	_ = json.Unmarshal(b, &event)
	if event.HookEventName != "" && event.HookEventName != "PostToolUse" {
		return nil
	}
	// A harness can run hooks for helper sessions (Muse's reminder agents);
	// only the worker's own session takes the message.
	name := event.HookEventName
	if name == "" {
		name = "PostToolUse"
	}
	return runHookReceipt(dir, event.SessionID, out, name)
}

// steerHooksOn reports whether the owner kept steering hooks on. When it is off,
// steering uses the interrupt path, but Muse keeps its memory denial.
func steerHooksOn() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv(EnvSteerHooks)), "off")
}

var steerHookProbes sync.Map // CLI path -> bool

func probeSteerHookSupport(h, path string) bool {
	var args []string
	var check func(string) bool
	switch h {
	case profile.HarnessCodex:
		args = []string{"exec", "--help"}
		check = func(s string) bool { return strings.Contains(s, "--dangerously-bypass-hook-trust") }
	case profile.HarnessMuse:
		args = []string{"--version"}
		check = func(s string) bool { return museVersionAtLeast(s, museHookMinVersion) }
	default:
		return false
	}
	if path == "" {
		return false
	}
	if v, ok := steerHookProbes.Load(path); ok {
		return v.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := childenv.Clean(exec.CommandContext(ctx, path, args...)).CombinedOutput()
	ok := err == nil && check(string(out))
	steerHookProbes.Store(path, ok)
	return ok
}

var museVersionRE = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

func museVersionAtLeast(s string, min [3]int) bool {
	m := museVersionRE.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		n, _ := strconv.Atoi(m[i+1])
		if n != min[i] {
			return n > min[i]
		}
	}
	return true
}

// tomlString renders s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// sign-in phases, as the shared in-app card shows them.
const (
	signInStarting  = "starting"
	signInWaiting   = "waiting"
	signInVerifying = "verifying"
	signInSucceeded = "succeeded"
	signInFailed    = "failed"
	signInCancelled = "cancelled"
)

// signInTimeout bounds the owner's device-code approval when a spec sets none.
const signInTimeout = 15 * time.Minute

// signInMessageLimit keeps a login error short enough for a phone card.
const signInMessageLimit = 200

// SignInState is the in-app view of one native sign-in. It holds the link and
// the one-time user code, which the owner needs, and never token contents.
type SignInState struct {
	Phase        string `json:"phase"`
	URL          string `json:"url,omitempty"`
	Code         string `json:"code,omitempty"`
	Message      string `json:"message,omitempty"`
	AcceptsPaste bool   `json:"acceptsPaste,omitempty"`
}

// final reports a phase the runner and the owner can no longer change.
func (s SignInState) final() bool {
	return s.Phase == signInSucceeded || s.Phase == signInFailed || s.Phase == signInCancelled
}

// signInSpec adapts one harness's login command to the shared runner. A later
// harness adds an argv change and an output parser, plus AcceptsPaste when its
// login reads a code from stdin; the runner itself keeps working as it is.
type signInSpec struct {
	// Argv adapts the command the account manager chose for the in-app flow.
	Argv func([]string) []string
	// Parse updates st from one output line, with ANSI codes already removed.
	Parse func(st *SignInState, line string)
	// AcceptsPaste keeps stdin open and shows a paste box.
	AcceptsPaste bool
	// Timeout bounds the wait for the owner. Zero uses signInTimeout.
	Timeout time.Duration
}

// signInSpecs lists the harnesses that sign in through the runner rather than a
// terminal. Every other harness keeps its terminal.
var signInSpecs = map[string]signInSpec{
	"codex": {Argv: codexDeviceArgv, Parse: parseCodexSignIn, Timeout: signInTimeout},
}

var (
	// ansiPattern matches the color and cursor sequences a CLI writes when it
	// cannot tell that its output is a pipe.
	ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)
	// codexDeviceURL accepts only OpenAI's device page, so a stray line is
	// never shown as a sign-in link.
	codexDeviceURL  = regexp.MustCompile(`^https://auth\.openai\.com/codex/device/?$`)
	codexDeviceCode = regexp.MustCompile(`^[A-Z0-9]{4,8}(?:-[A-Z0-9]{4,8})+$`)
)

// plainOutput strips terminal sequences and the indentation codex adds.
func plainOutput(line string) string {
	return strings.TrimSpace(ansiPattern.ReplaceAllString(line, ""))
}

// codexDeviceArgv asks codex for device-code sign-in, which prints a link and
// a code instead of starting a loopback callback.
func codexDeviceArgv(argv []string) []string {
	return append(append([]string(nil), argv...), "--device-auth")
}

// parseCodexSignIn reads the link and the one-time code that
// `codex login --device-auth` prints, then waits for the owner to approve.
func parseCodexSignIn(st *SignInState, line string) {
	switch {
	case codexDeviceURL.MatchString(line):
		st.URL = line
	case codexDeviceCode.MatchString(line):
		st.Code = line
	default:
		return
	}
	if st.URL != "" && st.Code != "" {
		st.Phase = signInWaiting
	}
}

// privateSignIn owns one login child process and the state its card shows.
type privateSignIn struct {
	mu     sync.Mutex
	state  SignInState
	stdin  io.WriteCloser
	cancel context.CancelFunc
	done   chan struct{}
	closed bool
}

// snapshot returns the current state. The pane polls it while a card is open.
func (s *privateSignIn) snapshot() SignInState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// set applies one change unless the flow already reached a final phase, so a
// late exit never overwrites a cancellation.
func (s *privateSignIn) set(fn func(*SignInState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.final() {
		return
	}
	fn(&s.state)
}

// respond sends a pasted code to a login that reads one from stdin.
func (s *privateSignIn) respond(code string) error {
	s.mu.Lock()
	stdin, closed, accepts := s.stdin, s.closed, s.state.AcceptsPaste
	s.mu.Unlock()
	if closed || stdin == nil || !accepts {
		return errors.New("This sign-in does not take a pasted code.")
	}
	if _, err := io.WriteString(stdin, code+"\n"); err != nil {
		return errors.New("The sign-in stopped before it could take the code.")
	}
	return nil
}

// stop kills the login child and its whole process group.
func (s *privateSignIn) stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// closeStdin stops a paste login once it no longer needs input.
func (s *privateSignIn) closeStdin() {
	s.mu.Lock()
	stdin := s.stdin
	s.closed = true
	s.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
}

// signInEnv merges the login environment over the engine's own, as the terminal
// path does, and drops DYNA_* so the child never inherits session state.
func signInEnv(overrides []string) []string {
	merged := map[string]string{}
	for _, item := range append(os.Environ(), overrides...) {
		key, value, ok := strings.Cut(item, "=")
		if ok && !strings.HasPrefix(key, "DYNA_") {
			merged[key] = value
		}
	}
	// The login prints plain text for the card, not a full-screen UI.
	merged["TERM"] = "dumb"
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+merged[key])
	}
	return out
}

// findPrivateLogin returns the running flow for an account, or nil.
func (a *Runtime) findPrivateLogin(id string) *privateRuntimeLogin {
	a.privateLoginMu.Lock()
	defer a.privateLoginMu.Unlock()
	return a.privateLogins[id]
}

// PrivateRuntimeLoginState returns the current step of one in-app sign-in.
func (a *Runtime) PrivateRuntimeLoginState(id string) (SignInState, error) {
	login := a.findPrivateLogin(id)
	if login == nil {
		return SignInState{}, errors.New("private sign-in is not running")
	}
	if login.signIn == nil {
		return SignInState{}, errors.New("this sign-in runs in a terminal")
	}
	return login.signIn.snapshot(), nil
}

// RespondPrivateRuntimeLogin sends the code the owner pasted to a login that
// reads one from stdin. The pane calls it only when AcceptsPaste is set.
func (a *Runtime) RespondPrivateRuntimeLogin(id, text string) error {
	login := a.findPrivateLogin(id)
	if login == nil {
		return errors.New("private sign-in is not running")
	}
	if login.signIn == nil {
		return errors.New("This sign-in does not take a pasted code.")
	}
	code := strings.TrimSpace(text)
	if code == "" || len(code) > 4096 || strings.ContainsAny(code, "\r\n") {
		return errors.New("Paste the sign-in code on one line.")
	}
	return login.signIn.respond(code)
}

// startPrivateSignIn runs a login command with pipes instead of a terminal, so
// the owner can finish signing in on a phone.
func (a *Runtime) startPrivateSignIn(id, harness string, spec signInSpec, flow *NativeLogin) (PrivateLogin, error) {
	argv := flow.Argv
	if spec.Argv != nil {
		argv = spec.Argv(argv)
	}
	if len(argv) == 0 || argv[0] == "" {
		_ = flow.Cancel()
		return PrivateLogin{}, errors.New("the sign-in command is unavailable")
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = signInTimeout
	}
	ctx, cancel := context.WithTimeout(a.lifetimeContext(), timeout)
	signIn := &privateSignIn{state: SignInState{Phase: signInStarting, AcceptsPaste: spec.AcceptsPaste}, cancel: cancel, done: make(chan struct{})}
	out, writer := io.Pipe()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = flow.Cwd
	cmd.Env = signInEnv(flow.Env)
	cmd.Stdout, cmd.Stderr = writer, writer
	// The child has no terminal, so it starts its own process group and a
	// cancel kills the whole group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	if spec.AcceptsPaste {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			cancel()
			_ = flow.Cancel()
			return PrivateLogin{}, err
		}
		signIn.stdin = stdin
	}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = writer.Close()
		_ = flow.Cancel()
		return PrivateLogin{}, fmt.Errorf("could not start the %s sign-in", harness)
	}
	a.privateLoginMu.Lock()
	if a.privateLogins == nil {
		a.privateLogins = map[string]*privateRuntimeLogin{}
	}
	login := &privateRuntimeLogin{flow: flow, signIn: signIn}
	a.privateLogins[id] = login
	a.privateLoginMu.Unlock()
	go a.watchPrivateSignIn(ctx, login, spec, cmd, out, writer, cancel)
	state := signIn.snapshot()
	return PrivateLogin{AccountID: id, Warning: flow.Warning, SignIn: &state}, nil
}

// watchPrivateSignIn parses the login output and settles the flow when the
// child exits. It never returns output beyond a short failure message.
func (a *Runtime) watchPrivateSignIn(ctx context.Context, login *privateRuntimeLogin, spec signInSpec, cmd *exec.Cmd, out *io.PipeReader, writer *io.PipeWriter, cancel context.CancelFunc) {
	signIn := login.signIn
	defer cancel()
	defer close(signIn.done)
	read := make(chan struct{})
	go func() {
		defer close(read)
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			line := plainOutput(scanner.Text())
			if line == "" || spec.Parse == nil {
				continue
			}
			signIn.set(func(st *SignInState) { spec.Parse(st, line) })
		}
	}()
	err := cmd.Wait()
	// Wait copies all output first, so closing the writer ends the scan.
	_ = writer.Close()
	<-read
	_ = out.Close()
	signIn.closeStdin()
	if signIn.snapshot().final() {
		// The owner cancelled, or the flow already settled.
		return
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			signIn.set(func(st *SignInState) { st.Message = "Sign-in timed out. Try again." })
		} else {
			signIn.set(func(st *SignInState) { st.Message = "Sign-in did not finish. Try again." })
		}
		signIn.set(func(st *SignInState) { st.Phase = signInFailed })
		_ = login.flow.Cancel()
		return
	}
	// Exit 0 means the owner approved. The account is saved now, so a closed
	// app still keeps the sign-in.
	signIn.set(func(st *SignInState) { st.Phase = signInVerifying })
	if _, completeErr := login.flow.Complete(a.lifetimeContext()); completeErr != nil {
		signIn.set(func(st *SignInState) { st.Message = signInFailure(completeErr) })
		signIn.set(func(st *SignInState) { st.Phase = signInFailed })
		_ = login.flow.Cancel()
		return
	}
	signIn.set(func(st *SignInState) { st.Phase = signInSucceeded })
}

// signInFailure shortens a login error for the card.
func signInFailure(err error) string {
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "Sign-in did not finish. Try again."
	}
	if runes := []rune(text); len(runes) > signInMessageLimit {
		text = strings.TrimSpace(string(runes[:signInMessageLimit]))
	}
	return text
}

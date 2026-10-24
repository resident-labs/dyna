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
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/terminalsvc"
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

// SignInChoice is one native OAuth provider or login-method choice.
type SignInChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// SignInState is the in-app view of one native sign-in. It holds the link and
// the one-time user code, which the owner needs, and never token contents.
type SignInState struct {
	Choices      []SignInChoice `json:"choices,omitempty"`
	Phase        string         `json:"phase"`
	URL          string         `json:"url,omitempty"`
	Code         string         `json:"code,omitempty"`
	Message      string         `json:"message,omitempty"`
	AcceptsPaste bool           `json:"acceptsPaste,omitempty"`
	SecretInput  bool           `json:"secretInput,omitempty"`
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
	// AcceptsPaste shows a paste box and keeps stdin open unless Respond is set.
	AcceptsPaste bool
	// SecretInput masks the paste field from the first published state.
	SecretInput bool
	// Respond handles a pasted response instead of writing it to stdin.
	Respond func(context.Context, SignInState, string) error
	// GracefulStop lets a helper stop and join children in separate groups.
	GracefulStop bool
	// Timeout bounds the wait for the owner. Zero uses signInTimeout.
	Timeout time.Duration
}

// signInSpecs lists the harnesses with an in-app sign-in adapter.
var signInSpecs = map[string]signInSpec{
	"pi":          {Argv: piOAuthArgv, Parse: parsePiSignIn, AcceptsPaste: true, GracefulStop: true},
	"antigravity": {Parse: parseAntigravitySignIn, AcceptsPaste: true, Respond: respondAntigravitySignIn, GracefulStop: true, Timeout: 5 * time.Minute},
	"claude-code": {Parse: parseClaudeCodeSignIn, AcceptsPaste: true},
	"codex":       {Argv: codexDeviceArgv, Parse: parseCodexSignIn, Timeout: signInTimeout},
	"grok":        {Argv: grokDeviceArgv, Parse: parseGrokSignIn},
	"kimi-code":   {Argv: kimiSignInArgv, Parse: parseKimiSignIn, AcceptsPaste: true, GracefulStop: true},
	"muse":        {Argv: museDeviceArgv, Parse: parseMuseSignIn},
	"opencode":    {Argv: openCodeDeviceArgv, Parse: parseOpenCodeSignIn},
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
	paste  func(SignInState, string) error
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

// respond sends a pasted response through its handler or the login stdin.
func (s *privateSignIn) respond(code string) error {
	s.mu.Lock()
	stdin, closed, st, paste := s.stdin, s.closed, s.state, s.paste
	if closed || !st.AcceptsPaste || st.final() {
		s.mu.Unlock()
		return errors.New("This sign-in does not take a pasted response.")
	}
	if paste != nil {
		if st.Phase != signInWaiting {
			s.mu.Unlock()
			return errors.New("Wait for the sign-in link before sending a response.")
		}
		s.state.Phase = signInVerifying
		s.mu.Unlock()
		if err := paste(st, code); err != nil {
			s.set(func(state *SignInState) { state.Phase = signInWaiting })
			return err
		}
		return nil
	}
	s.mu.Unlock()
	if stdin == nil {
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

// signInEnv merges the login environment over the engine's own and drops
// DYNA_* so the child never inherits session state.
func signInEnv(harness string, overrides []string) []string {
	// The login prints plain text for the card, not a full-screen UI.
	return childenv.Merge(NativeEnvironmentBase(harness, os.Environ()), overrides, "dumb")
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
		return SignInState{}, errors.New("private sign-in state is unavailable")
	}
	return login.signIn.snapshot(), nil
}

// RespondPrivateRuntimeLogin sends the owner's response through the paste
// handler or stdin. The pane calls it only when AcceptsPaste is set.
func (a *Runtime) RespondPrivateRuntimeLogin(id, text string) error {
	login := a.findPrivateLogin(id)
	if login == nil {
		return errors.New("private sign-in is not running")
	}
	if login.signIn == nil {
		return errors.New("This sign-in does not take a pasted code.")
	}
	code := strings.TrimSpace(text)
	limit := 4096
	message := "Paste the sign-in code or redirect URL on one line."
	if login.signIn.snapshot().SecretInput {
		limit = maxProviderKey
		message = "Enter the API key on one line."
	}
	if code == "" || len(code) > limit || strings.ContainsAny(code, "\r\n\x00") {
		return errors.New(message)
	}
	return login.signIn.respond(code)
}

// unavailableSignIn leaves a recoverable failed card without preparing credentials.
func (a *Runtime) unavailableSignIn(id, harness string) PrivateLogin {
	state := SignInState{Phase: signInFailed, Message: fmt.Sprintf("Sign-in is not available for %s.", harness)}
	done := make(chan struct{})
	close(done)
	a.privateLoginMu.Lock()
	defer a.privateLoginMu.Unlock()
	if a.privateLogins == nil {
		a.privateLogins = map[string]*privateRuntimeLogin{}
	}
	a.privateLogins[id] = &privateRuntimeLogin{signIn: &privateSignIn{state: state, done: done, closed: true}}
	return PrivateLogin{AccountID: id, SignIn: &state}
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
	signIn := &privateSignIn{state: SignInState{Phase: signInStarting, AcceptsPaste: spec.AcceptsPaste, SecretInput: spec.SecretInput}, cancel: cancel, done: make(chan struct{})}
	if spec.Respond != nil {
		signIn.paste = func(st SignInState, text string) error { return spec.Respond(ctx, st, text) }
	}
	out, writer := io.Pipe()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = flow.Cwd
	cmd.Env = signInEnv(harness, flow.Env)
	cmd.Stdout, cmd.Stderr = writer, writer
	// The child has no terminal, so it starts its own process group and a
	// cancel kills the whole group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if spec.GracefulStop {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	var stopTimer *time.Timer
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if spec.GracefulStop {
			// SIGTERM reaches the helper's context so it can join ACP before
			// exiting. A stuck helper loses all groups in its private session.
			stopTimer = time.AfterFunc(time.Second, func() { terminalsvc.KillSessionGroups(cmd.Process.Pid) })
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	if spec.AcceptsPaste && spec.Respond == nil {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			cancel()
			_ = writer.Close()
			_ = out.Close()
			_ = flow.Cancel()
			return PrivateLogin{}, err
		}
		signIn.stdin = stdin
	}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = writer.Close()
		_ = out.Close()
		_ = flow.Cancel()
		return PrivateLogin{}, fmt.Errorf("could not start the %s sign-in", harness)
	}
	a.privateLoginMu.Lock()
	if a.privateLogins == nil {
		a.privateLogins = map[string]*privateRuntimeLogin{}
	}
	login := &privateRuntimeLogin{flow: flow, signIn: signIn, harness: harness}
	a.privateLogins[id] = login
	a.privateLoginMu.Unlock()
	go func() {
		a.watchPrivateSignIn(ctx, login, spec, cmd, out, writer, cancel, func() {
			if stopTimer != nil {
				stopTimer.Stop()
			}
			if spec.GracefulStop {
				terminalsvc.KillSessionGroups(cmd.Process.Pid)
			}
		})
	}()
	state := signIn.snapshot()
	return PrivateLogin{AccountID: id, Warning: flow.Warning, SignIn: &state}, nil
}

// watchPrivateSignIn parses the login output and settles the flow when the
// child exits. It never returns output beyond a short failure message.
func (a *Runtime) watchPrivateSignIn(ctx context.Context, login *privateRuntimeLogin, spec signInSpec, cmd *exec.Cmd, out *io.PipeReader, writer *io.PipeWriter, cancel context.CancelFunc, stopped func()) {
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
	stopped()
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
	result, completeErr := login.flow.Complete(a.lifetimeContext())
	if completeErr == nil {
		// Capture can return no error and still leave no usable account, so
		// apply the same readiness rules as the Finish sign-in call.
		completeErr = loginReady(result)
	}
	if completeErr != nil {
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

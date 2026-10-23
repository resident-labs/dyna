package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func antigravityTestLink(redirect string) string {
	q := url.Values{"redirect_uri": {redirect}, "state": {"pending-state"}, "response_type": {"code"}}
	return "https://accounts.google.com/o/oauth2/v2/auth?" + q.Encode()
}

func TestAntigravitySignInReadsGoogleLink(t *testing.T) {
	spec, ok := signInSpecs["antigravity"]
	if !ok || spec.Parse == nil || !spec.AcceptsPaste {
		t.Fatal("Antigravity needs an in-app sign-in with a paste box")
	}
	st := SignInState{Phase: signInStarting}
	for _, line := range []string{
		antigravityTestLink("http://example.com:43210/"),
		antigravityTestLink("http://127.0.0.1:43210/other"),
		strings.Replace(antigravityTestLink("http://127.0.0.1:43210/"), "accounts.google.com", "example.com", 1),
	} {
		spec.Parse(&st, line)
		if st.URL != "" || st.Phase != signInStarting {
			t.Fatalf("unsafe link changed the sign-in: %+v", st)
		}
	}
	link := antigravityTestLink("http://127.0.0.1:43210/")
	spec.Parse(&st, link)
	if st.URL != link || st.Phase != signInWaiting || !strings.Contains(st.Message, "redirect URL") {
		t.Fatalf("state = %+v, want Google link and redirect instructions", st)
	}
}

func TestAntigravityCallbackMatchesWaitingSignIn(t *testing.T) {
	st := SignInState{URL: antigravityTestLink("http://127.0.0.1:43210/")}
	valid := "http://127.0.0.1:43210/?state=pending-state&code=one-time-code"
	for _, input := range []string{valid, strings.Replace(valid, "127.0.0.1", "localhost", 1)} {
		got, err := antigravityCallbackURL(st, input)
		if err != nil || got.Host != "127.0.0.1:43210" {
			t.Fatalf("valid callback %q = %v, %v", input, got, err)
		}
	}
	for _, input := range []string{
		strings.Replace(valid, "http:", "https:", 1),
		strings.Replace(valid, "127.0.0.1", "example.com", 1),
		strings.Replace(valid, "127.0.0.1", "127.0.0.2", 1),
		strings.Replace(valid, "127.0.0.1", "[::1]", 1),
		strings.Replace(valid, ":43210", ":43211", 1),
		strings.Replace(valid, ":43210", "", 1),
		strings.Replace(valid, "127.0.0.1", "user@127.0.0.1", 1),
		strings.Replace(valid, "/?", "/other?", 1),
		strings.Replace(valid, "pending-state", "wrong-state", 1),
		strings.Replace(valid, "code=one-time-code", "code=", 1),
		valid + "&state=pending-state",
		valid + "&code=another",
		valid + "&error=denied",
		valid + "#fragment",
		valid + "&iss=https://example.com",
	} {
		if _, err := antigravityCallbackURL(st, input); err == nil {
			t.Fatalf("unsafe callback was accepted: %q", input)
		}
	}
	if _, err := antigravityCallbackURL(SignInState{}, valid); err == nil {
		t.Fatal("callback accepted without a waiting authorization link")
	}
}

type callbackTransport func(*http.Request) (*http.Response, error)

func (f callbackTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAntigravityCallbackForwardsOnce(t *testing.T) {
	st := SignInState{URL: antigravityTestLink("http://127.0.0.1:43210/")}
	for _, status := range []int{http.StatusOK, http.StatusFound, http.StatusInternalServerError} {
		calls := 0
		transport := callbackTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != http.MethodGet || r.URL.Host != "127.0.0.1:43210" || r.URL.Query().Get("code") != "one-time-code" {
				t.Fatalf("forwarded callback = %s %s", r.Method, r.URL)
			}
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Location": {"https://example.com/"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		})
		err := forwardAntigravityCallback(context.Background(), st, "http://localhost:43210/?state=pending-state&code=one-time-code", transport)
		if (err == nil) != (status == http.StatusOK) || calls != 1 {
			t.Fatalf("status %d made %d requests with error %v", status, calls, err)
		}
	}
}

func TestPrivateSignInRoutesCustomPasteWithoutStdin(t *testing.T) {
	a := testPrivateRuntime(t)
	reached := make(chan struct{})
	var once sync.Once
	calls := 0
	link := antigravityTestLink("http://127.0.0.1:43210/")
	spec := signInSpec{
		Argv: func([]string) []string {
			// Stop after printing the link. Cancellation kills this process;
			// there is no stdin, sleep, polling, or timeout in this test.
			return []string{"/bin/sh", "-c", "printf '%s\n' '" + link + "'; kill -STOP $$"}
		},
		Parse:        waitingSignal(&once, reached, parseAntigravitySignIn),
		AcceptsPaste: true,
		Respond: func(ctx context.Context, st SignInState, text string) error {
			calls++
			state, err := a.PrivateRuntimeLoginState("acct")
			if err != nil || state.Phase != signInVerifying || st.URL != link || text != "callback" || ctx.Err() != nil {
				t.Errorf("custom paste state = %+v, input = %q, error = %v", state, text, err)
			}
			if calls == 1 {
				return errors.New("retry")
			}
			return nil
		},
	}
	flow := &NativeLogin{
		Cwd:    t.TempDir(),
		Cancel: func() error { return nil },
		Complete: func(context.Context) (NativeAccount, error) {
			t.Error("cancelled login must not complete")
			return NativeAccount{}, nil
		},
	}
	if _, err := a.startPrivateSignIn("acct", "antigravity", spec, flow); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CancelPrivateRuntimeLogin("acct") })
	login := a.findPrivateLogin("acct")
	<-reached
	if login.signIn.stdin != nil {
		t.Fatal("custom paste login opened stdin")
	}
	if err := a.RespondPrivateRuntimeLogin("acct", " callback "); err == nil || calls != 1 {
		t.Fatalf("custom paste calls = %d, error = %v", calls, err)
	}
	if state := login.signIn.snapshot(); state.Phase != signInWaiting {
		t.Fatalf("retry left state = %+v", state)
	}
	if err := a.RespondPrivateRuntimeLogin("acct", "callback"); err != nil {
		t.Fatal(err)
	}
	if state := login.signIn.snapshot(); state.Phase != signInVerifying || calls != 2 {
		t.Fatalf("successful paste left state = %+v, calls = %d", state, calls)
	}
	if err := a.RespondPrivateRuntimeLogin("acct", "callback"); err == nil || calls != 2 {
		t.Fatal("the custom handler accepted another response while verifying")
	}
	if err := a.CancelPrivateRuntimeLogin("acct"); err != nil {
		t.Fatal(err)
	}
	<-login.signIn.done
}

func TestSignInCleanupProcess(t *testing.T) {
	mode := os.Getenv("SIGNIN_CLEANUP_PROCESS")
	if mode == "" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	if mode == "child" {
		fmt.Println("ready")
		<-signals
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	child := exec.Command(exe, "-test.run=^TestSignInCleanupProcess$")
	child.Env = append(os.Environ(), "SIGNIN_CLEANUP_PROCESS=child")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := child.StdoutPipe()
	if err != nil || child.Start() != nil {
		os.Exit(2)
	}
	_, _ = bufio.NewReader(out).ReadString('\n')
	fmt.Printf("CHILD:%d\n", child.Process.Pid)
	<-signals
	_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
	_ = child.Wait()
	_ = os.WriteFile(os.Getenv("SIGNIN_CLEANUP_MARKER"), []byte("joined"), 0600)
	os.Exit(0)
}

func TestAntigravityCancelJoinsSeparateProcessGroupBeforeRestore(t *testing.T) {
	a := testPrivateRuntime(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "joined")
	reached := make(chan int, 1)
	spec := signInSpecs["antigravity"]
	spec.Argv = func([]string) []string { return []string{exe, "-test.run=^TestSignInCleanupProcess$"} }
	spec.Parse = func(st *SignInState, line string) {
		if raw, ok := strings.CutPrefix(line, "CHILD:"); ok {
			pid, _ := strconv.Atoi(raw)
			st.Phase = signInWaiting
			reached <- pid
		}
	}
	flow := &NativeLogin{
		Cwd: t.TempDir(),
		Env: []string{"SIGNIN_CLEANUP_PROCESS=parent", "SIGNIN_CLEANUP_MARKER=" + marker},
		Cancel: func() error {
			if _, err := os.Stat(marker); err != nil {
				t.Error("restored the login before the helper joined its separate process group")
			}
			return nil
		},
		Complete: func(context.Context) (NativeAccount, error) {
			t.Error("cancelled login must not complete")
			return NativeAccount{}, nil
		},
	}
	if _, err := a.startPrivateSignIn("acct", "antigravity", spec, flow); err != nil {
		t.Fatal(err)
	}
	pid := <-reached
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	t.Cleanup(func() { _ = a.CancelPrivateRuntimeLogin("acct") })
	if err := a.CancelPrivateRuntimeLogin("acct"); err != nil {
		t.Fatal(err)
	}
}

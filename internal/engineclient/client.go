// Package engineclient calls the engine's web API over its private Unix socket.
package engineclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"dyna-agent/internal/enginesocket"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/callspec"
)

var ErrNotRunning = errors.New("Dyna engine is not running")

// RemoteError is an error the engine returned for a call, with the reply's
// HTTP status.
type RemoteError struct {
	Status  int
	Message string
	// Code is the reply's machine-readable name, such as callspec.CodeEngineStopping.
	Code string
}

// Final reports whether a retry of the call would get the same answer: the
// engine refused it or found it invalid (4xx). A 5xx reply (a busy database,
// a deadline, an engine that is stopping or read-only) may succeed later.
func (e *RemoteError) Final() bool { return e.Status >= 400 && e.Status < 500 }

// NoMethod reports whether the engine has no such method: an engine of an
// earlier release than the caller.
func (e *RemoteError) NoMethod() bool { return e.Status == http.StatusNotFound }

func (e *RemoteError) Error() string { return e.Message }

// SocketPath is shared by the engine listener and the CLI transport.
func SocketPath() (string, error) { return enginesocket.Path() }

// Running reports whether an engine accepts connections on its socket.
// path "" means the default socket.
func Running(path string) bool {
	if path == "" {
		var err error
		if path, err = SocketPath(); err != nil {
			return false
		}
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// dialTimeout bounds connecting to the socket, and deadline each call.
// Tests shorten them.
var (
	dialTimeout = time.Second
	deadline    = callspec.Deadline
)

type Client struct {
	http        *http.Client
	restartWait time.Duration
	// now and after are the clock; tests replace them.
	now     func() time.Time
	after   func(time.Duration) <-chan time.Time
	notices io.Writer
	build   string
	// warned makes a build gap print one line per client, not per call.
	warned sync.Once
}

// Option changes a client made by New.
type Option func(*Client)

// RestartWait sets how long a call waits for an engine that is down or
// restarting. New uses 60 s.
func RestartWait(d time.Duration) Option { return func(c *Client) { c.restartWait = d } }

// Notices sets where the client writes its notices, or nil for nowhere: one
// line when a call starts to wait for the engine, and one line when the
// engine's build differs. New uses os.Stderr.
func Notices(w io.Writer) Option { return func(c *Client) { c.notices = w } }

// Build sets the build the client sends. New uses callspec.Build.
func Build(v string) Option { return func(c *Client) { c.build = v } }

// New uses the engine socket when path is empty. An explicit path supports
// isolated callers, including tests that must never reach the owner's engine.
func New(path string, opts ...Option) *Client {
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			socket := path
			if socket == "" {
				var err error
				socket, err = SocketPath()
				if err != nil {
					return nil, ErrNotRunning
				}
			}
			conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "unix", socket)
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
				return nil, fmt.Errorf("%w; tried socket %s", ErrNotRunning, socket)
			}
			return conn, err
		},
	}
	c := &Client{http: &http.Client{Transport: transport}, restartWait: time.Minute, now: time.Now, after: time.After, notices: os.Stderr, build: callspec.Build}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Call uses the same method route, argument array and result as the browser.
// While the engine is down, restarting (no socket, or the connection is
// refused) or running its startup migrations (a 503 with
// callspec.CodeEngineStarting), it retries for up to the restart wait, every
// 100 ms rising to 1 s. A call the engine accepted is never retried: past its
// deadline a read fails with "engine did not answer", and a write fails with
// "outcome unknown", because the engine may still apply it.
//
// A call without a request ID gets one. When the engine closes the connection
// before it answers (a restart for an update), a call that repeatsAfterCutOff
// is sent once more with the same ID.
func (c *Client) Call(ctx context.Context, method string, args []any, result any) error {
	if args == nil {
		args = []any{}
	}
	if callspec.RequestOf(ctx) == "" && !ownsRequestID(method) {
		id, err := newRequestID()
		if err != nil {
			return err
		}
		ctx = callspec.WithRequest(ctx, id)
	}
	cutOff, lateRepeated := false, false
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	deadline := deadline(method)
	giveUp := c.now().Add(c.restartWait)
	for delay := 100 * time.Millisecond; ; delay = min(2*delay, time.Second) {
		err := c.call(ctx, method, body, deadline, result)
		if callspec.IsCutOff(err) && !cutOff && ctx.Err() == nil && repeatsAfterCutOff(method) {
			// The restart wait starts here, not at the first send: the call
			// may have run for minutes before the engine stopped.
			cutOff = true
			giveUp = c.now().Add(c.restartWait)
			delay = 50 * time.Millisecond
			continue
		}
		if callspec.IsLate(err) && repeatsAfterLate(method) {
			if !lateRepeated && ctx.Err() == nil {
				// The engine runs calls with one request ID one at a time, so
				// this repeat waits for the first and returns its result.
				lateRepeated = true
				continue
			}
			return fmt.Errorf("%w; the engine may still apply it, so check before you repeat it", err)
		}
		if callspec.IsLate(err) && callspec.WriteCalls[method] {
			return fmt.Errorf("outcome unknown: %w; the engine may still apply %s, so read the state it changes before you repeat it", err, method)
		}
		var remote *RemoteError
		starting := errors.As(err, &remote) && remote.Code == callspec.CodeEngineStarting
		if !errors.Is(err, ErrNotRunning) && !starting {
			return err
		}
		left := giveUp.Sub(c.now())
		if left <= 0 {
			return fmt.Errorf("%w (waited %g s)", err, c.restartWait.Seconds())
		}
		if c.notices != nil && delay == 100*time.Millisecond {
			fmt.Fprintln(c.notices, "dyna: waiting for the engine to start")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.after(min(delay, left)):
		}
	}
}

func (c *Client) call(ctx context.Context, method string, body []byte, deadline time.Duration, result any) error {
	callCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	err := c.send(callCtx, method, body, result)
	if err != nil && ctx.Err() == nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return callspec.Late(method, deadline)
	}
	return err
}

func (c *Client) send(ctx context.Context, method string, body []byte, result any) error {
	target := url.URL{Scheme: "http", Host: "engine", Path: "/api/" + method}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// TODO: legacy/shim remove later — preserves the CLI guard only for unverified report-only calls; remove with caller enforcement (g3419).
	req.Header.Set(callspec.ClaimHeader, processClaim())
	if c.build != "" {
		req.Header.Set(callspec.VersionHeader, c.build)
	}
	if id := callspec.RequestOf(ctx); id != "" {
		req.Header.Set(callspec.RequestHeader, id)
	}
	res, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, ErrNotRunning) {
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				return urlErr.Err
			}
		}
		return err
	}
	defer res.Body.Close()
	// A gap alone is no failure: worktree and QA builds differ from the
	// engine's all the time. Say it once.
	if engine := res.Header.Get(callspec.VersionHeader); engine != "" && c.build != "" && engine != c.build && c.notices != nil {
		c.warned.Do(func() { fmt.Fprintln(c.notices, "dyna: "+callspec.VersionGap(engine, c.build)) })
	}
	if res.StatusCode != http.StatusOK {
		var reply struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.NewDecoder(res.Body).Decode(&reply); err != nil {
			return fmt.Errorf("engine: %s", res.Status)
		}
		return &RemoteError{Status: res.StatusCode, Message: reply.Error, Code: reply.Code}
	}
	if result == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(result)
}

// ownsRequestID reports calls whose request ID has a meaning of its own: a
// session's runner calls and run records carry IDs the engine checks against
// the caller, and a wait for input takes none.
func ownsRequestID(method string) bool {
	switch method {
	case "FinishRun", "AddRunDirectories", "AdmitNativeAttempt", "ObserveClaudeUsage":
		return true
	}
	return strings.HasPrefix(method, "Runner")
}

func newRequestID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "cli-" + hex.EncodeToString(b[:]), nil
}

// repeatsAfterCutOff names the calls that are safe to send again after the
// engine cut the connection. StartSubagent carries a session ID the caller
// chose, and the engine refuses a second session with it. Other writes commit
// before their receipt is stored, so a repeat could apply them twice.
// repeatsAfterLate lists the creating calls that go again once, with their
// request ID, when the engine is slow to answer. The engine applies one ID once,
// so the repeat returns the created record instead of making a second one.
func repeatsAfterLate(method string) bool { return method == "AddGoal" || method == "AddGoalItem" }

func repeatsAfterCutOff(method string) bool { return method == "StartSubagent" }

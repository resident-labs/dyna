package harness

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/jsonrpc"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/scratch"
)

const codexCommandTimeout = 30 * time.Second

// This is one attached protocol client, not a process registry. The shared
// Host owns the process, its leases, replacement and idle close.
type codexSessionProcess struct {
	host         sessionhost.Session
	conn         *jsonrpc.Conn
	transport    *nativeSessionTransport
	thread       string
	shellServers map[string]bool
	exited       chan struct{}
	activity     chan struct{}
	idle         bool
	mu           sync.Mutex
	events       []jsonrpc.Notification
	mcpErr       error
}

func codexSessionProcessFor(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, execution ExecutionOptions) (*codexSessionProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	execution.SessionKey = spec.codexKey
	launch, err := nativeSessionLaunch(p, cwd, spec, execution)
	if err != nil {
		return nil, err
	}
	launch.Key = spec.codexKey + "\x00" + environmentValue(launch.Env, "CODEX_HOME")
	launch.Argv, err = codexSessionHookArgs(p, launch.Key, launch.Argv, launch.Env)
	if err != nil {
		return nil, err
	}
	launch.Fingerprint = sessionLaunchFingerprint(launch.Argv[0], launch.Env, launch.Argv) + "\x00" + launch.Dir
	hosted, err := nativeSessionHost.Session(ctx, launch)
	if err != nil {
		return nil, err
	}
	s := &codexSessionProcess{host: hosted, exited: make(chan struct{}), activity: make(chan struct{}, 1), shellServers: codexShellMCPServers(spec.argv)}
	s.transport = newNativeSessionTransport(hosted)
	s.conn = jsonrpc.NewConn(s.transport, jsonrpc.Handlers{Notification: s.route, Request: codexServerRequest}, func() { close(s.exited) })
	failed := true
	defer func() {
		if failed {
			_ = hosted.Close()
			s.detach()
		}
	}()
	err = s.request(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "dyna_worker", "version": "1"}}, nil)
	cold := err == nil
	var rpcErr *jsonrpc.Error
	if err != nil && !(errors.As(err, &rpcErr) && rpcErr.ErrorCode() == -32600 && rpcErr.Message == "Already initialized") {
		return nil, err
	}
	if err = s.conn.Notify("initialized", nil); err != nil {
		return nil, err
	}
	if !cold && spec.codexThread != "" {
		// This Host key names one Dyna session. An initialized live process
		// already has this thread loaded; a cold process must resume it below.
		s.thread = spec.codexThread
		failed = false
		return s, nil
	}
	params := spec.codexPolicy.params(true)
	params["cwd"] = cwd
	if spec.codexPolicy.cwd != "" {
		params["cwd"] = spec.codexPolicy.cwd
	}
	if p.Model != "" {
		params["model"] = p.Model
	}
	method := "thread/start"
	if spec.codexThread != "" {
		method = "thread/resume"
		params["threadId"] = spec.codexThread
	} else {
		params["ephemeral"] = spec.codexPolicy.ephemeral
	}
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err = s.request(ctx, method, params, &result); err != nil {
		return nil, err
	}
	if result.Thread.ID == "" || (spec.codexThread != "" && result.Thread.ID != spec.codexThread) {
		return nil, errors.New("Codex did not return the requested thread")
	}
	s.thread = result.Thread.ID
	if err = s.mcpFailure(); err != nil {
		return nil, err
	}
	failed = false
	return s, nil
}

func codexServerRequest(_ context.Context, method string, _ json.RawMessage) (any, error) {
	// Headless workers have no approval UI. Keep restricted profiles restricted.
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]any{"decision": "decline"}, nil
	case "item/tool/requestUserInput":
		return map[string]any{"answers": map[string]any{}}, nil
	case "mcpServer/elicitation/request":
		return map[string]any{"action": "cancel"}, nil
	default:
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: fmt.Sprintf("unsupported Codex request %s", method)}
	}
}

// Hook config stays stable across attachments. The scratch owner removes it
// with the session; native steering never leaves pending hook messages.
func codexSessionHookArgs(p profile.Profile, key string, argv, env []string) ([]string, error) {
	if !steerHookSupport(p.Harness, p.RuntimeExecutable) || !steerHooksOn() {
		return argv, nil
	}
	root := environmentValue(env, scratch.Env)
	hash := sha256.Sum256([]byte(key))
	if root == "" {
		var err error
		root, err = scratch.Ensure(fmt.Sprintf("codex-%x", hash[:12]))
		if err != nil {
			return nil, err
		}
	}
	dir := filepath.Join(root, "codex-hooks", fmt.Sprintf("%x", hash[:12]))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	command, err := steerHookCommand(dir)
	if err != nil {
		return nil, err
	}
	hook := &steerHook{harness: p.Harness, dir: dir, command: command, steer: true}
	return hook.argv(argv, env)
}

func (s *codexSessionProcess) request(ctx context.Context, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, codexCommandTimeout)
	defer cancel()
	return s.conn.Request(ctx, method, params, out)
}
func (s *codexSessionProcess) route(u jsonrpc.Notification) {
	s.mu.Lock()
	s.events = append(s.events, u)
	if s.mcpErr == nil {
		s.mcpErr = codexMCPStartupFailure(u, s.shellServers)
	}
	s.mu.Unlock()
	select {
	case s.activity <- struct{}{}:
	default:
	}
}
func (s *codexSessionProcess) drain() []jsonrpc.Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := s.events
	s.events = nil
	return events
}
func (s *codexSessionProcess) detach() {
	s.conn.Close()
	reason := sessionhost.DetachAbandoned
	if s.idle {
		reason = sessionhost.DetachIdle
	}
	s.host.Detach(reason)
}
func (s *codexSessionProcess) exitError() error { return s.transport.exitError() }
func (s *codexSessionProcess) kill()            { _ = s.host.Close(); <-s.exited }

// mcpFailure reports a Dyna shell MCP server that failed to start. A turn
// without those tools cannot do Dyna work, so it stops at once.
func (s *codexSessionProcess) mcpFailure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mcpErr
}

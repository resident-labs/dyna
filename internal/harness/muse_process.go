package harness

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/jsonrpc"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/scratch"
	"github.com/google/uuid"
)

const museCommandTimeout = 30 * time.Second

// MSP requires UUIDv7 command IDs, separate from JSON-RPC request IDs.
func museCommandID() (string, error) { id, err := uuid.NewV7(); return id.String(), err }

type museSessionProcess struct {
	host      sessionhost.Session
	conn      *jsonrpc.Conn
	transport *nativeSessionTransport
	session   string
	exited    chan struct{}
	activity  chan struct{}
	idle      bool
	mu        sync.Mutex
	events    []jsonrpc.Notification
	decisions map[string]string
}

func museSessionProcessFor(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, execution ExecutionOptions) (*museSessionProcess, error) {
	execution.SessionKey = spec.museKey
	launch, err := nativeSessionLaunch(p, cwd, spec, execution)
	if err != nil {
		return nil, err
	}
	launch.Argv, err = museSessionHookArgs(launch.Key, launch.Argv, launch.Env)
	if err != nil {
		return nil, err
	}
	launch.Fingerprint = sessionLaunchFingerprint(launch.Argv[0], launch.Env, launch.Argv) + "\x00" + launch.Dir
	cold := false
	started := launch.OnStart
	launch.OnStart = func(pid int) {
		cold = true
		if started != nil {
			started(pid)
		}
	}
	hosted, err := nativeSessionHost.Session(ctx, launch)
	if err != nil {
		return nil, err
	}
	s := &museSessionProcess{host: hosted, session: spec.museSession, exited: make(chan struct{}), activity: make(chan struct{}, 1)}
	s.transport = newNativeSessionTransport(hosted)
	s.conn = jsonrpc.NewConn(s.transport, jsonrpc.Handlers{Notification: s.route, Request: s.serverRequest}, func() { close(s.exited) })
	failed := true
	defer func() {
		if failed {
			_ = hosted.Close()
			s.detach()
		}
	}()
	if cold {
		var initialized struct {
			Schema struct {
				Version     int    `json:"version"`
				Fingerprint string `json:"fingerprint"`
			} `json:"schema"`
		}
		if err = s.request(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "dyna_worker", "title": "Dyna", "version": "1"}, "capabilities": map[string]any{"requestedCapabilities": []string{}}}, &initialized); err != nil {
			return nil, err
		}
		if initialized.Schema.Version != 1 {
			return nil, fmt.Errorf("Muse initialize returned an unsupported schema version")
		}
		if err = s.conn.Notify("initialized", map[string]any{}); err != nil {
			return nil, err
		}
	}
	if cold || !spec.museResume {
		method := "session/start"
		params := map[string]any{"sessionId": spec.museSession}
		if spec.museResume {
			method = "session/resume"
			params["excludeItems"] = true
			params["permissionProfile"] = spec.musePermission
			params["approvalMode"] = spec.museApproval
		} else {
			params["workspaceRoot"] = cwd
			params["permissionProfile"] = spec.musePermission
			params["approvalMode"] = spec.museApproval
			if p.Model != "" {
				params["modelId"] = p.Model
			}
		}
		var result struct {
			Session struct {
				ID string `json:"sessionId"`
			} `json:"session"`
		}
		if err = s.command(ctx, method, params, &result); err != nil {
			return nil, err
		}
		if result.Session.ID != spec.museSession {
			return nil, fmt.Errorf("Muse did not return the requested session")
		}
		if spec.museResume {
			if err = s.command(ctx, "session/setApprovalMode", map[string]any{"mode": spec.museApproval}, nil); err != nil {
				return nil, err
			}
		}
	}
	if spec.museResume && p.Model != "" {
		if err = s.command(ctx, "session/setModel", map[string]any{"model": map[string]string{"modelId": p.Model}}, nil); err != nil {
			return nil, err
		}
	}
	failed = false
	return s, nil
}
func (s *museSessionProcess) request(ctx context.Context, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, museCommandTimeout)
	defer cancel()
	return s.conn.Request(ctx, method, params, out)
}
func (s *museSessionProcess) command(ctx context.Context, method string, params map[string]any, out any) error {
	id, err := museCommandID()
	if err != nil {
		return err
	}
	params["commandId"] = id
	if _, ok := params["sessionId"]; !ok {
		params["sessionId"] = s.session
	}
	var raw json.RawMessage
	if err = s.request(ctx, method, params, &raw); err != nil {
		return err
	}
	var ack struct {
		CommandID string `json:"commandId"`
	}
	if err = json.Unmarshal(raw, &ack); err != nil {
		return err
	}
	if ack.CommandID != "" && ack.CommandID != id {
		return fmt.Errorf("Muse %s acknowledged a different command", method)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
func (s *museSessionProcess) route(n jsonrpc.Notification) {
	s.mu.Lock()
	s.events = append(s.events, n)
	s.mu.Unlock()
	select {
	case s.activity <- struct{}{}:
	default:
	}
}
func (s *museSessionProcess) drain() []jsonrpc.Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := s.events
	s.events = nil
	return events
}
func (s *museSessionProcess) detach() {
	s.conn.Close()
	reason := sessionhost.DetachAbandoned
	if s.idle {
		reason = sessionhost.DetachIdle
	}
	s.host.Detach(reason)
}
func (s *museSessionProcess) exitError() error { return s.transport.exitError() }
func (s *museSessionProcess) kill()            { _ = s.host.Close(); s.conn.Close() }

// A server request acknowledges presentation only. The matching notification
// carries the pending decision, which the turn loop resolves as a command.
func (s *museSessionProcess) serverRequest(_ context.Context, method string, _ json.RawMessage) (any, error) {
	switch method {
	case "approval/request", "userInput/request":
		return map[string]any{}, nil
	}
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "unsupported Muse request: " + method}
}
func (s *museSessionProcess) answerHeadless(ctx context.Context, n jsonrpc.Notification) error {
	if n.Method != "approval/requested" && n.Method != "approval/updated" && n.Method != "userInput/requested" {
		return nil
	}
	var p struct {
		SessionID   string          `json:"sessionId"`
		ApprovalID  string          `json:"approvalId"`
		UserInputID string          `json:"userInputId"`
		Requirement json.RawMessage `json:"currentRequirementId"`
		Choices     []struct {
			ID       string `json:"choiceId"`
			Decision string `json:"decision"`
		} `json:"availableChoices"`
	}
	if err := json.Unmarshal(n.Params, &p); err != nil {
		return err
	}
	if p.SessionID != s.session {
		return nil
	}
	if n.Method == "approval/updated" && (len(p.Requirement) == 0 || string(p.Requirement) == "null") {
		return nil
	}
	if n.Method == "userInput/requested" {
		return s.command(ctx, "userInput/cancel", map[string]any{"userInputId": p.UserInputID, "reason": "Dyna workers run without an interactive input surface"}, nil)
	}
	// Even unrestricted profiles must not turn an explicit pending approval into
	// an implicit grant. Native permission policy decides what needs approval.
	if s.decisions == nil {
		s.decisions = map[string]string{}
	}
	requirement := string(p.Requirement)
	if s.decisions[p.ApprovalID] == requirement {
		return nil
	}
	for _, decision := range []string{"denied", "deniedPolicyAmendment", "abort"} {
		for _, choice := range p.Choices {
			if choice.Decision == decision {
				err := s.command(ctx, "approval/decide", map[string]any{"approvalId": p.ApprovalID, "requirementId": p.Requirement, "choiceId": choice.ID}, nil)
				if err == nil {
					s.decisions[p.ApprovalID] = requirement
				}
				return err
			}
		}
	}
	return fmt.Errorf("Muse approval has no decline choice")
}

// The memory denial hook belongs to the process, while steering uses MSP.
func museSessionHookArgs(key string, argv, env []string) ([]string, error) {
	root := environmentValue(env, scratch.Env)
	hash := sha256.Sum256([]byte(key))
	if root == "" {
		var err error
		root, err = scratch.Ensure(fmt.Sprintf("muse-%x", hash[:12]))
		if err != nil {
			return nil, err
		}
	}
	dir := filepath.Join(root, "muse-hooks", fmt.Sprintf("%x", hash[:12]))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	hook := &steerHook{harness: profile.HarnessMuse, dir: dir}
	return hook.argv(argv, env)
}

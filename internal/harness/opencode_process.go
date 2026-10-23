package harness

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/profile"
)

const openCodeCommandTimeout = 30 * time.Second

type openCodeConnection struct {
	URL      string `json:"url"`
	Password string `json:"password"`
}
type openCodeEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}
type openCodeSessionProcess struct {
	host       sessionhost.Session
	connection openCodeConnection
	client     *http.Client
	session    string
	cancel     context.CancelFunc
	exited     chan struct{}
	activity   chan struct{}
	mu         sync.Mutex
	events     []openCodeEvent
	err        error
	idle       bool
}

func openCodeSessionProcessFor(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, execution ExecutionOptions) (_ *openCodeSessionProcess, err error) {
	execution.SessionKey = spec.openCodeKey
	launch, err := nativeSessionLaunch(p, cwd, spec, execution)
	if err != nil {
		return nil, err
	}
	// Random connection values do not change launch identity on the next turn.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		return nil, err
	}
	connection := openCodeConnection{URL: fmt.Sprintf("http://127.0.0.1:%d", port), Password: hex.EncodeToString(secret[:])}
	launch.Argv = append(launch.Argv, "--hostname", "127.0.0.1", "--port", fmt.Sprint(port))
	launch.Env = append(launch.Env, "OPENCODE_PASSWORD="+connection.Password, "OPENCODE_SERVER_PASSWORD=")
	launch.Connection, _ = json.Marshal(connection)
	hosted, err := nativeSessionHost.Session(ctx, launch)
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(context.Background())
	s := &openCodeSessionProcess{host: hosted, client: &http.Client{}, session: spec.openCodeSession, cancel: cancel, exited: make(chan struct{}), activity: make(chan struct{}, 1)}
	defer func() {
		if err != nil {
			_ = hosted.Close()
			s.detach()
		}
	}()
	retained, ok := hosted.(sessionhost.ConnectionSession)
	if !ok {
		return nil, fmt.Errorf("OpenCode host has no connection data")
	}
	if err = json.Unmarshal(retained.Connection(), &s.connection); err != nil {
		return nil, err
	}
	// Consume stdout continuously, and stop the HTTP stream if the owned child exits.
	go func() {
		for {
			if _, e := hosted.Next(streamCtx); e != nil {
				if streamCtx.Err() == nil {
					s.fail(e)
				}
				return
			}
		}
	}()
	readyCtx, readyCancel := context.WithTimeout(ctx, openCodeCommandTimeout)
	defer readyCancel()
	for {
		var info struct {
			Version string `json:"version"`
		}
		err = s.request(readyCtx, http.MethodGet, "/api/info", nil, &info)
		if err == nil && info.Version != "" {
			break
		}
		select {
		case <-readyCtx.Done():
			return nil, fmt.Errorf("OpenCode server startup: %w", readyCtx.Err())
		case <-s.activity:
			s.mu.Lock()
			err = s.err
			s.mu.Unlock()
			if err != nil {
				return nil, err
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	// Subscribe before creating or prompting a session; events are not replayed.
	req, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, s.connection.URL+"/api/event", nil)
	req.SetBasicAuth("opencode", s.connection.Password)
	req.Header.Set("Accept", "text/event-stream")
	response, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("OpenCode event stream: HTTP %d", response.StatusCode)
	}
	go s.readEvents(response.Body)
	permissions := []map[string]string{{"action": "*", "resource": "*", "effect": "allow"}}
	if p.SafeMode {
		permissions[0]["effect"] = "ask"
	}
	model := openCodeModel(p)
	if s.session == "" {
		body := map[string]any{"location": map[string]string{"directory": cwd}, "title": "Dyna worker", "permissions": permissions}
		if model != nil {
			body["model"] = model
		}
		var created struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err = s.request(ctx, http.MethodPost, "/api/session", body, &created); err != nil {
			return nil, err
		}
		if created.Data.ID == "" {
			return nil, fmt.Errorf("OpenCode created no session")
		}
		s.session = created.Data.ID
	} else {
		if err = s.request(ctx, http.MethodGet, s.path(""), nil, nil); err != nil {
			return nil, err
		}
		if err = s.request(ctx, http.MethodPatch, s.path(""), map[string]any{"permissions": permissions}, nil); err != nil {
			return nil, err
		}
		if model != nil {
			if err = s.request(ctx, http.MethodPost, s.path("/model"), map[string]any{"model": model}, nil); err != nil {
				return nil, err
			}
		}
	}
	// A retained server must run tools with the current turn's private environment.
	variables := map[string]string{}
	for _, pair := range launch.Env {
		key, value, ok := strings.Cut(pair, "=")
		if ok && key != "OPENCODE_PASSWORD" && key != "OPENCODE_SERVER_PASSWORD" {
			variables[key] = value
		}
	}
	if err = s.request(ctx, http.MethodPut, s.path("/environment"), map[string]any{"variables": variables}, nil); err != nil {
		return nil, err
	}
	return s, nil
}
func openCodeModel(p profile.Profile) map[string]string {
	if p.Model == "" {
		return nil
	}
	provider, id, ok := strings.Cut(p.Model, "/")
	if !ok {
		return nil
	}
	id, variant, _ := strings.Cut(id, "#")
	model := map[string]string{"providerID": provider, "id": id}
	if v, ok := argumentValue(p.ExtraArgs, "--variant"); ok {
		variant = v
	}
	if variant != "" {
		model["variant"] = variant
	}
	return model
}
func (s *openCodeSessionProcess) path(suffix string) string {
	return "/api/session/" + s.session + suffix
}
func (s *openCodeSessionProcess) request(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, openCodeCommandTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.connection.URL+path, reader)
	if err != nil {
		return err
	}
	req.SetBasicAuth("opencode", s.connection.Password)
	req.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("OpenCode %s %s: transport failed", method, path)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		return fmt.Errorf("OpenCode %s %s: HTTP %d: %s", method, path, response.StatusCode, b)
	}
	if out != nil {
		if target, ok := out.(*strings.Builder); ok {
			_, err := io.Copy(target, response.Body)
			if err != nil {
				return err
			}
			return openCodeWaitResponse(target.String())
		}
		return json.NewDecoder(response.Body).Decode(out)
	}
	_, err = io.Copy(io.Discard, response.Body)
	return err
}
func (s *openCodeSessionProcess) readEvents(body io.ReadCloser) {
	defer body.Close()
	defer close(s.exited)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				var event openCodeEvent
				if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &event); err != nil {
					s.fail(fmt.Errorf("invalid OpenCode event: %w", err))
					return
				}
				s.mu.Lock()
				s.events = append(s.events, event)
				s.mu.Unlock()
				s.wake()
				data = nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		s.fail(fmt.Errorf("OpenCode event stream: %w", err))
	} else {
		s.fail(fmt.Errorf("OpenCode event stream ended"))
	}
}
func (s *openCodeSessionProcess) wake() {
	select {
	case s.activity <- struct{}{}:
	default:
	}
}
func (s *openCodeSessionProcess) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.wake()
}
func (s *openCodeSessionProcess) drain() []openCodeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := s.events
	s.events = nil
	return events
}
func (s *openCodeSessionProcess) exitError() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *openCodeSessionProcess) detach() {
	s.cancel()
	s.client.CloseIdleConnections()
	reason := sessionhost.DetachAbandoned
	if s.idle {
		reason = sessionhost.DetachIdle
	}
	s.host.Detach(reason)
}
func (s *openCodeSessionProcess) kill() { _ = s.host.Close(); s.cancel() }
func (s *openCodeSessionProcess) prompt(ctx context.Context, text, delivery, id string) error {
	return s.request(ctx, http.MethodPost, s.path("/prompt"), map[string]any{"id": id, "text": text, "delivery": delivery}, nil)
}
func (s *openCodeSessionProcess) answerHeadless(ctx context.Context, e openCodeEvent, safe bool) error {
	var data struct {
		Session string `json:"sessionID"`
		ID      string `json:"id"`
		Form    struct {
			ID      string `json:"id"`
			Session string `json:"sessionID"`
		} `json:"form"`
	}
	if json.Unmarshal(e.Data, &data) != nil {
		return nil
	}
	if data.Session == "" {
		data.Session = data.Form.Session
	}
	if data.Session != s.session {
		return nil
	}
	switch e.Type {
	case "permission.asked":
		decision := "once"
		if safe {
			decision = "reject"
		}
		return s.request(ctx, http.MethodPost, s.path("/permission/"+data.ID+"/reply"), map[string]string{"decision": decision}, nil)
	case "form.created":
		if data.Form.ID != "" {
			data.ID = data.Form.ID
		}
		return s.request(ctx, http.MethodDelete, s.path("/form/"+data.ID), nil, nil)
	}
	return nil
}

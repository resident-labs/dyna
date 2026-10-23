package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

type openCodeTestHost struct {
	connection []byte
	env        []string
	starts     int
	alive      bool
}

func (h *openCodeTestHost) Session(_ context.Context, l sessionhost.Launch) (sessionhost.Session, error) {
	h.env = l.Env
	if !h.alive {
		h.alive = true
		h.starts++
		if l.OnStart != nil {
			l.OnStart(123)
		}
	}
	return h, nil
}
func (h *openCodeTestHost) Connection() []byte { return h.connection }
func (h *openCodeTestHost) Write([]byte) error { return io.ErrClosedPipe }
func (h *openCodeTestHost) Next(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (h *openCodeTestHost) Replace(context.Context, sessionhost.Launch) error {
	return io.ErrClosedPipe
}
func (h *openCodeTestHost) Detach(r sessionhost.DetachReason) {
	if r == sessionhost.DetachAbandoned {
		h.alive = false
	}
}
func (h *openCodeTestHost) Close() error { h.alive = false; return nil }

func TestOpenCodeServeReusesSteersAndInterrupts(t *testing.T) {
	installFakeCLI(t, "opencode", "#!/bin/sh\nexit 1\n")
	var mu sync.Mutex
	var events chan string
	var calls []string
	expectedDecision := "once"
	emit := func(kind string, data map[string]any) {
		if kind != "form.created" {
			data["sessionID"] = "ses_fixture"
		}
		b, _ := json.Marshal(map[string]any{"type": kind, "data": data})
		events <- string(b)
	}
	finish := func() {
		emit("session.tool.input.started", map[string]any{"id": "tool", "name": "shell"})
		emit("session.tool.called", map[string]any{"id": "tool", "input": map[string]string{"command": "true"}})
		emit("session.tool.success", map[string]any{"id": "tool", "content": []map[string]string{{"type": "text", "text": "done"}}})
		emit("session.text.ended", map[string]any{"assistantMessageID": "message", "text": "done"})
		emit("session.step.ended", map[string]any{"assistantMessageID": "message", "finish": "stop", "tokens": map[string]any{"input": 10, "output": 5, "reasoning": 2, "cache": map[string]int{"read": 3, "write": 0}}})
		emit("session.execution.succeeded", map[string]any{})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "opencode" || pass != "secret" {
			t.Error("missing private Basic authentication")
			http.Error(w, "auth", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/event" {
			mu.Lock()
			events = make(chan string, 32)
			e := events
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			for {
				select {
				case s := <-e:
					fmt.Fprintf(w, "data: %s\n\n", s)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/info":
			fmt.Fprint(w, `{"version":"2.0.32"}`)
		case "/api/session":
			fmt.Fprint(w, `{"data":{"id":"ses_fixture"}}`)
		case "/api/session/ses_fixture/prompt":
			var p map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			fmt.Fprint(w, `{"id":"inbox"}`)
			if p["text"] != "task" && p["text"] != "hold" {
				emit("session.execution.succeeded", map[string]any{})
			}
			emit("session.inbox.delivered", map[string]any{"inboxID": p["id"]})
			emit("session.execution.started", map[string]any{})
			emit("session.reasoning.ended", map[string]any{"text": "plan", "assistantMessageID": "message"})
			if p["text"] == "task" {
				emit("permission.asked", map[string]any{"id": "ask"})
			} else if p["text"] != "hold" {
				finish()
			}
		case "/api/session/ses_fixture/permission/ask/reply":
			var p map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			if p["decision"] != expectedDecision {
				t.Errorf("decision=%v", p)
			}
			emit("form.created", map[string]any{"form": map[string]string{"id": "question", "sessionID": "ses_fixture"}})
			w.WriteHeader(204)
		case "/api/session/ses_fixture/form/question":
			w.WriteHeader(204)
			finish()
		case "/api/session/ses_fixture/interrupt":
			fmt.Fprint(w, `{"interrupted":true}`)
			emit("session.execution.interrupted", map[string]any{})
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()
	connection, _ := json.Marshal(map[string]string{"url": server.URL, "password": "secret"})
	host := &openCodeTestHost{connection: connection}
	old := nativeSessionHost
	nativeSessionHost = host
	t.Cleanup(func() { nativeSessionHost = old })
	p := profile.Profile{Harness: profile.HarnessOpenCode, RuntimeExecutable: "/bin/false"}
	cwd := t.TempDir()
	normalized := filepath.Join(cwd, "normalized.jsonl")
	if err := os.WriteFile(normalized, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var id string
	run := func(prompt string, steering SteeringOptions, ctx context.Context) (Result, error) {
		return RunWithOptions(ctx, p, prompt, cwd, false, JournalOptions{NormalizedOutputPath: normalized}, steering, SessionOptions{ResumeID: id, OnSession: func(s string) { id = s }}, ExecutionOptions{SessionKey: "opencode-test"})
	}
	for range 2 {
		got, err := run("task", SteeringOptions{}, t.Context())
		if err != nil || got.Output != "done" || got.Usage.InputTokens != 13 || got.Usage.OutputTokens != 7 {
			t.Fatalf("turn=%+v error=%v", got, err)
		}
	}
	expectedDecision = "reject"
	p.SafeMode = true
	if _, err := run("task", SteeringOptions{}, t.Context()); err != nil {
		t.Fatal(err)
	}
	p.SafeMode = false
	expectedDecision = "once"
	delivered := false
	got, err := run("hold", SteeringOptions{Source: &armedSource{armed: true, message: "finish"}, OnMessage: func(_ runstore.SteeringMessage, boundary *int64) { delivered = true }}, t.Context())
	if err != nil || got.Output != "done" || !delivered {
		t.Fatalf("steer=%+v error=%v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err = run("hold", SteeringOptions{Source: &armedSource{}, OnStart: cancel}, ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupt=%v", err)
	}
	if host.starts != 1 || !host.alive {
		t.Fatalf("retained process: starts=%d alive=%v", host.starts, host.alive)
	}
	raw, err := os.ReadFile(normalized)
	if err != nil || !strings.Contains(string(raw), `"name":"shell"`) {
		t.Fatalf("tool metadata lost: %s, %v", raw, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Count(strings.Join(calls, "\n"), "POST /api/session\n") != 1 {
		t.Fatalf("session recreated: %v", calls)
	}
}

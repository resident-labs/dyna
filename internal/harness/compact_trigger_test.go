package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
)

// The ACP server has no compact slash command. A configured manual
// threshold must fail before an ordinary model prompt can receive /compact.
func TestAntigravityACPDoesNotTreatCompactAsModelInput(t *testing.T) {
	_, err := buildInvocation(profile.Profile{
		Harness: profile.HarnessAntigravity, Compact: &profile.Compact{Tokens: 1},
	}, "task")
	if err == nil || !strings.Contains(err.Error(), "not supported by Antigravity ACP 1.3.0") {
		t.Fatalf("manual compaction was not refused: %v", err)
	}
}

// The service owns usage and compaction state; a client can miss usage events.
func TestOpenCodeCompactReadsStoredContextAndWaitsBeforeResume(t *testing.T) {
	log, steering := installOpenCodeCompactCLI(t)
	p := profile.Profile{Name: "oc-fake", Harness: profile.HarnessOpenCode, Compact: &profile.Compact{Tokens: 120, Windows: map[string]int64{"": 200000}}, Env: map[string]string{"DYNA_FAKE_LOG": log}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := RunSessionTurn(ctx, p, "first", t.TempDir(), false, JournalOptions{}, steering, SessionOptions{})
	if err != nil || first.SessionID == "" {
		t.Fatalf("first=%+v, %v", first, err)
	}
	next, err := RunSessionTurn(ctx, p, "next", t.TempDir(), false, JournalOptions{}, steering, SessionOptions{ResumeID: first.SessionID})
	if err != nil || next.CompactError != "" {
		t.Fatalf("next=%+v, %v", next, err)
	}
	// The conversation shows Dyna's own compaction with trigger dyna and the
	// context size that made it due.
	if c := next.Compaction; c == nil || c.State != "completed" || c.Before != 140 {
		t.Fatalf("compaction = %+v, want completed at 140 tokens", c)
	}
	calls := strings.Split(strings.TrimSpace(readTestFile(t, log)), "\n")
	want := []string{"run --format", "api session.context", "api experimental.session.wait", "api session.compact", "api experimental.session.wait", "run --format"}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls=%q, want %q", calls, want)
	}
}

func installOpenCodeCompactCLI(t *testing.T) (string, SteeringOptions) {
	t.Helper()
	installFakeCLI(t, "opencode", "#!/bin/sh\nexit 1\n")
	log := filepath.Join(t.TempDir(), "requests")
	host := &openCodeTestHost{}
	var events chan string
	var waits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/event" {
			events = make(chan string, 8)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			for {
				select {
				case event := <-events:
					fmt.Fprintf(w, "data: %s\n\n", event)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
		record := func(line string) {
			f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Error(err)
				return
			}
			defer f.Close()
			fmt.Fprintln(f, line)
		}
		env := func(key string) string { return environmentValue(host.env, key) }
		switch {
		case r.URL.Path == "/api/info":
			fmt.Fprint(w, `{"version":"2.0.32"}`)
		case r.URL.Path == "/api/session":
			fmt.Fprint(w, `{"data":{"id":"ses_fixture"}}`)
		case strings.HasSuffix(r.URL.Path, "/context"):
			record("api session.context")
			fmt.Fprint(w, `{"data":[{"type":"assistant","tokens":{"input":100,"cache":{"read":30,"write":10}}}]}`)
		case strings.HasSuffix(r.URL.Path, "/wait"):
			record("api experimental.session.wait")
			waits++
			mode := env("OC_WAIT_RESULT")
			if mode == "missing" {
				http.Error(w, "missing", 404)
			} else if mode == "changed" || mode == "changed-after" && waits > 1 {
				fmt.Fprint(w, `{"unexpected":true}`)
			} else {
				w.WriteHeader(204)
			}
		case strings.HasSuffix(r.URL.Path, "/compact"):
			record("api session.compact")
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/prompt"):
			record("run --format")
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if path := env("OC_PROMPT_LOG"); path != "" {
				os.WriteFile(path, []byte(body["text"]), 0600)
			}
			fmt.Fprint(w, `{"id":"inbox"}`)
			events <- fmt.Sprintf(`{"type":"session.inbox.delivered","data":{"sessionID":"ses_fixture","inboxID":%q}}`, body["id"])
			events <- `{"type":"session.text.ended","data":{"sessionID":"ses_fixture","text":"done"}}`
			events <- `{"type":"session.execution.succeeded","data":{"sessionID":"ses_fixture"}}`
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	host.connection, _ = json.Marshal(map[string]string{"url": server.URL, "password": "secret"})
	old := nativeSessionHost
	nativeSessionHost = host
	t.Cleanup(func() { nativeSessionHost = old; server.Close() })
	return log, SteeringOptions{}
}

func TestOpenCodeCompactSkipsMissingOrChangedWaitEndpoint(t *testing.T) {
	for _, mode := range []string{"missing", "changed", "changed-after"} {
		t.Run(mode, func(t *testing.T) {
			log, steering := installOpenCodeCompactCLI(t)
			p := profile.Profile{Name: "oc-fake", Harness: profile.HarnessOpenCode, Compact: &profile.Compact{Tokens: 120}, RuntimeInstructions: "RESTORED_RULE_CANARY", Env: map[string]string{"DYNA_FAKE_LOG": log, "OC_WAIT_RESULT": mode, "OPENCODE_CONFIG_CONTENT": "{}", "OC_PROMPT_LOG": log + ".prompt"}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result, err := RunSessionTurn(ctx, p, "continue safely", t.TempDir(), false, JournalOptions{}, steering, SessionOptions{ResumeID: "ses_fixture"})
			if err != nil {
				t.Fatalf("ordinary turn failed: %v", err)
			}
			if !strings.Contains(result.CompactError, "native automatic compaction stays enabled") || strings.Contains(result.CompactError, "\n") {
				t.Fatalf("want one clear compact warning, got %q", result.CompactError)
			}
			// The conversation records Dyna's failed compaction, with the context
			// size that made it due and the reason it failed.
			if c := result.Compaction; c == nil || c.State != "failed" || c.Before != 140 || c.Reason == "" {
				t.Fatalf("compaction = %+v, want failed at 140 tokens with a reason", c)
			}
			calls := readTestFile(t, log)
			admitted := mode == "changed-after"
			if strings.Contains(calls, "api session.compact") != admitted || !strings.Contains(calls, "run --format") {
				t.Fatalf("unsafe API sequence: %s", calls)
			}
			prompt := readTestFile(t, log+".prompt")
			if strings.Contains(prompt, "RESTORED_RULE_CANARY") != admitted {
				t.Fatalf("instruction restoration does not match possible admission: %q", prompt)
			}
		})
	}
}

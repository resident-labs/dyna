package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"dyna-agent/internal/questions"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/voice/realtime"
)

func TestCallSessionGivesVoiceItsToolsAndSettings(t *testing.T) {
	tools, _, _ := toolsFixture(t)
	s := CallSession(context.Background(), tools)
	var names []string
	for _, tool := range s.Tools {
		names = append(names, tool.Name)
		if !json.Valid(tool.Parameters) || tool.Description == "" {
			t.Errorf("tool %s has no description or bad parameters", tool.Name)
		}
	}
	if want := []string{"dispatch", "remain_silent"}; !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
	if s.Voice != "marin" || s.Language != "en" || s.TranscriptionPrompt != TranscriptionPrompt || !strings.Contains(s.Instructions, "dispatch") {
		t.Errorf("session = %+v, want marin, en, the keyword prompt and Voice's instructions", s)
	}
}

func TestDispatchToolRequiresTheOwnersPrompt(t *testing.T) {
	tools, _, _ := toolsFixture(t)
	if _, err := run(t, tools.Realtime(context.Background()), context.Background(), "dispatch", `{}`); err == nil {
		t.Fatal("dispatch without a prompt answered, want an error")
	}
}

func run(t *testing.T, tools []realtime.Tool, ctx context.Context, name, args string) (string, error) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool.Run(ctx, json.RawMessage(args))
		}
	}
	t.Fatalf("no tool %s", name)
	return "", nil
}

// A call whose provider connection drops right after a tool call dials the
// provider again instead of ending.
func TestCallRedialsWhenTheProviderDropsAfterAToolCall(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	externalRoot(t, work)
	var dials atomic.Int32
	redialed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		var m map[string]any
		_ = ws.ReadJSON(&m) // session.update
		_ = ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.updated"}`))
		if dials.Add(1) > 1 {
			close(redialed)
			for ws.ReadJSON(&m) == nil {
			}
			return
		}
		_ = ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.function_call_arguments.done","call_id":"c1","name":"dispatch","arguments":"{\"prompt\":\"check CI\"}"}`))
		for ws.ReadJSON(&m) == nil {
			if m["type"] == "conversation.item.create" {
				return // dispatch ran; the connection drops
			}
		}
	}))
	defer srv.Close()
	p := realtime.OpenAI("ws"+strings.TrimPrefix(srv.URL, "http"), func(context.Context) (string, string, error) { return "tok", "acct", nil })
	ctx, hangUp := context.WithCancel(t.Context())
	sess, err := runCall(ctx, tools.link, p)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-redialed:
	case <-sess.done:
		t.Fatalf("the call ended instead of redialing: %v", sess.err)
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not redial")
	}
	hangUp()
	<-sess.done
}

// Voice's context never describes handing the work to someone else.
func TestVoiceSpeaksAsTheOneDoingTheWork(t *testing.T) {
	tools, _, _ := toolsFixture(t)
	s := CallSession(context.Background(), tools)
	read := []string{s.Instructions, workStarted, tellPrompt("CI is green.", nil), questionPrompt(questions.Question{Text: "Merge PR 7?", Options: []string{"yes", "no"}})}
	for _, tool := range s.Tools {
		read = append(read, tool.Name, tool.Description)
	}
	for _, text := range read {
		lower := strings.ToLower(text)
		for _, bad := range []string{"root", "pass it on", "passed it on", "passed to", "pass that along", "can't confirm"} {
			if strings.Contains(lower, bad) {
				t.Errorf("Voice reads %q, which contains %q", text, bad)
			}
		}
	}
}

// The call uses the provider the voice settings choose; speech-to-speech
// needs its server's address.
func TestChosenProviderFollowsTheVoiceSettings(t *testing.T) {
	load := func(provider, url string) func(string) (settings.Values, error) {
		return func(string) (settings.Values, error) {
			v := settings.Defaults()
			v.VoiceProvider, v.VoiceURL = provider, url
			return v, nil
		}
	}
	if p, err := chosenProvider(load("openai", "")); err != nil || p.Key != "openai" {
		t.Fatalf("openai: %q, %v", p.Key, err)
	}
	p, err := chosenProvider(load("speech-to-speech", "ws://box:8000/v1/realtime"))
	if err != nil || p.Key != "speech-to-speech" {
		t.Fatalf("speech-to-speech: %q, %v", p.Key, err)
	}
	if url, _, err := p.Endpoint(context.Background()); err != nil || url != "ws://box:8000/v1/realtime" {
		t.Fatalf("endpoint %q, %v", url, err)
	}
	if _, err := chosenProvider(load("speech-to-speech", "")); err == nil || !strings.Contains(err.Error(), "voice.url") {
		t.Fatalf("no address: %v", err)
	}
	// DashScope uses its default model and the key in DASHSCOPE_API_KEY.
	t.Setenv("DASHSCOPE_API_KEY", "sk-test")
	p, err = chosenProvider(load("dashscope", ""))
	if err != nil || p.Key != "dashscope" {
		t.Fatalf("dashscope: %q, %v", p.Key, err)
	}
	if url, h, err := p.Endpoint(context.Background()); err != nil || !strings.Contains(url, "model=qwen-audio-3.0-realtime-plus") || h.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("dashscope endpoint %q %v, %v", url, h, err)
	}
	// Google Live reads GOOGLE_API_KEY, else GEMINI_API_KEY.
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "AIza-test")
	p, err = chosenProvider(load("google-live", ""))
	if err != nil || p.Key != "google-live" {
		t.Fatalf("google-live: %q, %v", p.Key, err)
	}
	if url, _, err := p.Endpoint(context.Background()); err != nil || !strings.Contains(url, "key=AIza-test") {
		t.Fatalf("google-live endpoint %q, %v", url, err)
	}
	t.Setenv("DOUBAO_API_KEY", "")
	t.Setenv("SEEDUPLEX_API_KEY", "dk-test")
	p, err = chosenProvider(load("doubao-seeduplex", ""))
	if err != nil || p.Key != "doubao-seeduplex" {
		t.Fatalf("doubao: %q, %v", p.Key, err)
	}
	if _, h, err := p.Endpoint(context.Background()); err != nil || h.Get("X-Api-Key") != "dk-test" {
		t.Fatalf("doubao header %v, %v", h, err)
	}
	t.Setenv("STEPFUN_API_KEY", "sf-test")
	p, err = chosenProvider(load("stepfun", ""))
	if err != nil || p.Key != "stepfun" {
		t.Fatalf("stepfun: %q, %v", p.Key, err)
	}
	if url, h, err := p.Endpoint(context.Background()); err != nil || !strings.Contains(url, "model=stepaudio-3-realtime-preview") || h.Get("Authorization") != "Bearer sf-test" {
		t.Fatalf("stepfun endpoint %q %v, %v", url, h, err)
	}
	if _, err := chosenProvider(load("minicpm-o", "")); err == nil || !strings.Contains(err.Error(), "voice.url") {
		t.Fatalf("minicpm-o without an address: %v", err)
	}
	if p, err = chosenProvider(load("minicpm-o", "ws://box:32550/ws")); err != nil || p.Key != "minicpm-o" {
		t.Fatalf("minicpm-o: %q, %v", p.Key, err)
	}
}

// Of the owner's tool set, dispatch alone answers later, and the removed
// checks are gone from the prompt too.
func TestOnlyDispatchAnswersLater(t *testing.T) {
	tools, _, _ := toolsFixture(t)
	var later []string
	for _, tool := range CallSession(context.Background(), tools).Tools {
		if tool.Later {
			later = append(later, tool.Name)
		}
	}
	if !slices.Equal(later, []string{"dispatch"}) {
		t.Fatalf("tools that answer later %v, want only dispatch", later)
	}
	for _, gone := range []string{"check_agents", "check_recent_activity"} {
		if strings.Contains(prompt, gone) {
			t.Errorf("the prompt still names %s", gone)
		}
	}
}

func TestCallConnectionJoinsRequestOnlyAfterSuccessfulDial(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	st := tools.link.svc.CallRequestStore()
	request, _, err := st.Ask(work.ID, "CI needs your sign-in", "root")
	if err != nil {
		t.Fatal(err)
	}
	failed := realtime.OpenAI("ws://unused", func(context.Context) (string, string, error) { return "", "", fmt.Errorf("sign-in unavailable") })
	if _, err := runCall(t.Context(), tools.link, failed); err == nil {
		t.Fatal("failed dial started a call")
	}
	pending, err := st.Get(work.ID, request.ID)
	if err != nil || pending.State != "ringing" {
		t.Fatalf("failed dial closed the request: %+v %v", pending, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		var message json.RawMessage
		if ws.ReadJSON(&message) != nil {
			return
		}
		if ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.updated"}`)) != nil {
			return
		}
		for ws.ReadJSON(&message) == nil {
		}
	}))
	defer srv.Close()
	provider := realtime.OpenAI("ws"+strings.TrimPrefix(srv.URL, "http"), func(context.Context) (string, string, error) { return "token", "account", nil })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sess, err := runCall(ctx, tools.link, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); <-sess.done }()
	joined, err := st.Get(work.ID, request.ID)
	if err != nil || joined.State != "joined" {
		t.Fatalf("connected call did not join: %+v %v", joined, err)
	}
}

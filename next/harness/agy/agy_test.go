package agy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/agy/fakeagent"
	"github.com/resident-labs/dyna/harness/pig"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeagent.EnvVar) != "" {
		fakeagent.Run(os.Stdin, os.Stdout)
		return
	}
	os.Exit(m.Run())
}

// rig is one test's agent: its message log and the hooks' calls.
type rig struct {
	log     string
	notices chan string
}

// open starts a harness on the fake agent, which is this test binary run again as `agy acp`.
func open(t *testing.T, env map[string]string, edit func(*Config)) (*Harness, *rig, Config) {
	t.Helper()
	r := &rig{log: filepath.Join(t.TempDir(), "agent.log"), notices: make(chan string, 8)}
	t.Setenv(fakeagent.EnvVar, "1")
	t.Setenv("FAKE_AGY_LOG", r.log)
	for k, v := range env {
		t.Setenv(k, v)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := Config{
		Binary: exe, GeminiDir: t.TempDir(), Dir: t.TempDir(), SessionDir: t.TempDir(), New: true,
		Model:         contract.ModelChoice{ID: "gemini-3.8-flash", Thinking: contract.ThinkingLow},
		Instructions:  "be Dyna",
		MCPServers:    []MCPServer{{Name: "dyna", URL: "http://127.0.0.1:7781/dyna/", Headers: map[string]string{"Authorization": "Bearer x"}}},
		CancelTimeout: 300 * time.Millisecond,
		Notice:        func(s string) { r.notices <- s },
	}
	if edit != nil {
		edit(&c)
	}
	h, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h, r, c
}

type logged struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

func (r *rig) messages(t *testing.T) []logged {
	t.Helper()
	data, _ := os.ReadFile(r.log)
	var out []logged
	for _, line := range bytes.Split(data, []byte("\n")) {
		var m logged
		if json.Unmarshal(line, &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func (r *rig) calls(t *testing.T, method string) []logged {
	var out []logged
	for _, m := range r.messages(t) {
		if m.Method == method {
			out = append(out, m)
		}
	}
	return out
}

// prompts are the text blocks of each session/prompt the agent received.
func (r *rig) prompts(t *testing.T) [][]string {
	var out [][]string
	for _, m := range r.calls(t, "session/prompt") {
		var p struct{ Prompt []struct{ Text string } }
		json.Unmarshal(m.Params, &p)
		var texts []string
		for _, b := range p.Prompt {
			texts = append(texts, b.Text)
		}
		out = append(out, texts)
	}
	return out
}

func input(delivery int64, text string) contract.TurnInput {
	return contract.TurnInput{Delivery: contract.DeliverySeq(delivery), Text: text}
}

func start(t *testing.T, h *Harness, turn string, inputs ...contract.TurnInput) contract.TurnEnded {
	t.Helper()
	res, err := h.Start(context.Background(), contract.TurnID(turn), inputs, func() {})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return res
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTurnWithToolCallsMirrorsPiTranscript(t *testing.T) {
	h, r, c := open(t, nil, nil)
	var mu sync.Mutex
	var labels []string
	var finished []contract.ToolFinished
	h.Activity = func(_ contract.TurnID, kind contract.ActivityKind, label string) {
		mu.Lock()
		defer mu.Unlock()
		if kind == contract.ActivityTool {
			labels = append(labels, label)
		}
	}
	h.Finished = func(_ contract.TurnID, f contract.ToolFinished) {
		mu.Lock()
		finished = append(finished, f)
		mu.Unlock()
	}
	res := start(t, h, "turn1", input(1, "tool"))
	if res.Outcome != contract.TurnCompleted || res.Final != "tool done" {
		t.Fatalf("result = %+v", res)
	}
	// Usage: input = inputTokens + cachedReadTokens (T3), the rest as reported.
	if u := res.Usage; u.Model != "gemini-3.8-flash" || u.InputTokens != 150 || u.OutputTokens != 20 || u.CacheReadTokens != 50 || u.CacheWriteTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
	mu.Lock()
	if len(labels) != 1 || labels[0] != "run_command ls" || len(finished) != 1 || finished[0].Call != "call_1" {
		t.Fatalf("activity %v finished %v", labels, finished)
	}
	mu.Unlock()
	file, err := os.Open(h.SessionFile())
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	items, _, _, err := pig.ReadTranscript(file, map[contract.DeliverySeq]contract.TurnID{1: "turn1"}, "agy", "", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Kind != contract.TranscriptText || items[0].Text != "looking" ||
		items[1].Kind != contract.TranscriptTool || items[1].Tool != "run_command" || items[1].Output != "a.txt\n" || items[1].Ended == nil ||
		items[2].Text != "tool done" {
		t.Fatalf("items = %+v", items)
	}
	if strings.Contains(items[1].Arguments, "toolSummary") || !strings.Contains(items[1].Arguments, `"CommandLine":"ls"`) {
		t.Fatalf("arguments = %s", items[1].Arguments)
	}
	// agy asked permission for the command and was allowed.
	eventually(t, "the permission answer", func() bool {
		for _, m := range r.messages(t) {
			if string(m.ID) == `"perm-ls"` && strings.Contains(string(m.Result), `"optionId":"allow"`) {
				return true
			}
		}
		return false
	})
	// Launch and session parameters.
	var init struct {
		ClientInfo struct{ Name string }
	}
	json.Unmarshal(r.calls(t, "initialize")[0].Params, &init)
	if init.ClientInfo.Name != "dyna" {
		t.Fatalf("clientInfo = %+v", init)
	}
	var session struct {
		Cwd        string
		McpServers []struct{ Type, Name, URL string }
		Meta       struct {
			Antigravity struct{ SystemPrompt struct{ Append string } }
		} `json:"_meta"`
	}
	json.Unmarshal(r.calls(t, "session/new")[0].Params, &session)
	if session.Cwd != c.Dir || len(session.McpServers) != 1 || session.McpServers[0].Type != "http" || session.McpServers[0].URL != "http://127.0.0.1:7781/dyna/" ||
		session.Meta.Antigravity.SystemPrompt.Append != "" {
		t.Fatalf("session/new = %+v", session)
	}
}

func TestOpenNewAndResume(t *testing.T) {
	h, r, c := open(t, nil, nil)
	start(t, h, "t1", input(1, "say hello"))
	h.Close()
	meta, err := readMeta(c.SessionDir)
	if err != nil || meta.SessionID != "fake-session-1" || meta.Mirror != "agy-1.jsonl" {
		t.Fatalf("meta = %+v, %v", meta, err)
	}
	// A second harness resumes the stored conversation and sets the model again, as agy's cold resume reports its default.
	c.New = false
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	var resume struct{ SessionID string }
	json.Unmarshal(r.calls(t, "session/resume")[0].Params, &resume)
	if resume.SessionID != "fake-session-1" || h2.SessionFile() != filepath.Join(c.SessionDir, "agy-1.jsonl") {
		t.Fatalf("resume %+v, file %s", resume, h2.SessionFile())
	}
	var options []string
	for _, m := range r.calls(t, "session/set_config_option") {
		options = append(options, string(m.Params))
	}
	if len(options) != 4 || !strings.Contains(options[2], `"configId":"model"`) || !strings.Contains(options[3], `"value":"low"`) {
		t.Fatalf("set_config_option calls = %v", options)
	}
	h2.Close()
	// T3 has no fallback: a session agy cannot resume fails the open.
	t.Setenv("FAKE_AGY_MISSING", "fake-session-1")
	if _, err := Open(context.Background(), c); err == nil || !strings.Contains(err.Error(), "session/resume failed") {
		t.Fatalf("open = %v", err)
	}
}

func TestSessionSetupAuthenticatesAgainOnAuthRequired(t *testing.T) {
	_, r, _ := open(t, map[string]string{"FAKE_AGY_REAUTH": "1"}, nil)
	if n := len(r.calls(t, "authenticate")); n != 2 {
		t.Fatalf("authenticate calls = %d", n)
	}
	if n := len(r.calls(t, "session/new")); n != 2 {
		t.Fatalf("session/new calls = %d", n)
	}
}

func TestCancelledContextCancelsThePrompt(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	running := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := h.Start(ctx, "t1", []contract.TurnInput{input(1, "hold")}, func() { close(running) })
		done <- err
	}()
	<-running
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls(t, "session/cancel")) != 1 {
		t.Fatal("the prompt was left running")
	}
}

func TestSteerSendsSecondPrompt(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	if err := h.Steer(context.Background(), input(9, "x")); !errors.Is(err, harness.ErrNoTurn) {
		t.Fatalf("steer without a turn = %v", err)
	}
	running := make(chan struct{})
	done := make(chan contract.TurnEnded, 1)
	go func() {
		res, _ := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "hold")}, func() { close(running) })
		done <- res
	}()
	<-running
	if err := h.Steer(context.Background(), input(2, "change course")); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if res.Outcome != contract.TurnCompleted || !strings.Contains(res.Final, "after steer") || res.Usage.OutputTokens != 40 {
		t.Fatalf("result = %+v", res)
	}
	if got := r.prompts(t); len(got) != 2 || !strings.Contains(got[1][0], "change course") || len(r.calls(t, "session/cancel")) != 0 {
		t.Fatalf("prompts = %v", got)
	}
	for _, id := range []contract.DeliverySeq{1, 2} {
		if ok, err := h.Delivered(context.Background(), id); !ok || err != nil {
			t.Fatalf("delivered %d = %v, %v", id, ok, err)
		}
	}
	if ok, _ := h.Delivered(context.Background(), 3); ok {
		t.Fatal("delivery 3 was never sent")
	}
}

func TestVersionBelow151IsRefused(t *testing.T) {
	t.Setenv(fakeagent.EnvVar, "1")
	t.Setenv("FAKE_AGY_VERSION", "agy_acp_server_1.4.5")
	exe, _ := os.Executable()
	if _, err := Open(context.Background(), Config{Binary: exe, GeminiDir: t.TempDir(), Dir: t.TempDir(), SessionDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "1.5.1") {
		t.Fatalf("err = %v", err)
	}
	for v, refused := range map[string]bool{"1.5.0": true, "v1.5.1": false, "1.6.0-beta": false, "agy_acp_server_1.5.18": false, "": false, "weird": false} {
		if (versionRefused(v) != nil) != refused {
			t.Errorf("%q refused = %v", v, !refused)
		}
	}
}

func TestFollowUpIsHeldUntilTheResponse(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	res, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "say first one")}, func() {
		if err := h.FollowUp(context.Background(), input(2, "say second one")); err != nil {
			t.Error(err)
		}
		if err := h.FollowUp(context.Background(), input(3, "say third one")); err != nil {
			t.Error(err)
		}
	})
	if err != nil || res.Final != "first one\n\nsecond one\n\nthird one" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if got := r.prompts(t); len(got) != 3 || got[1][0] != "say second one" {
		t.Fatalf("prompts = %v", got)
	}
	if err := h.FollowUp(context.Background(), input(4, "x")); !errors.Is(err, harness.ErrNoTurn) {
		t.Fatalf("follow-up after the turn = %v", err)
	}
}

func TestClearQueueDropsHeldFollowUps(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	_, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "say only")}, func() {
		h.FollowUp(context.Background(), input(2, "say never"))
		h.ClearQueue(context.Background())
	})
	if err != nil || len(r.prompts(t)) != 1 {
		t.Fatalf("prompts = %v, %v", r.prompts(t), err)
	}
	if ok, _ := h.Delivered(context.Background(), 2); ok {
		t.Fatal("a cleared follow-up was delivered")
	}
}

func TestAbortCancelsThenWaitsForTheResponse(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	running := make(chan struct{})
	done := make(chan contract.TurnEnded, 1)
	go func() {
		res, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "hold")}, func() { close(running) })
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	<-running
	if err := h.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.Outcome != contract.TurnStopped {
		t.Fatalf("result = %+v", res)
	}
	if len(r.calls(t, "session/cancel")) != 1 {
		t.Fatal("no session/cancel")
	}
	if len(r.calls(t, "session/new")) != 1 {
		t.Fatal("the process was restarted although agy answered the cancel")
	}
}

func TestAbortKillsAnAgentThatIgnoresCancelAndTheNextTurnResumes(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	running := make(chan struct{})
	done := make(chan contract.TurnEnded, 1)
	go func() {
		res, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "stubborn")}, func() { close(running) })
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	<-running
	began := time.Now()
	h.Abort(context.Background())
	if res := <-done; res.Outcome != contract.TurnStopped {
		t.Fatalf("result = %+v", res)
	}
	if time.Since(began) < 250*time.Millisecond {
		t.Fatal("killed before the cancel timeout")
	}
	res := start(t, h, "t2", input(2, "say again"))
	if res.Outcome != contract.TurnCompleted || res.Final != "again" {
		t.Fatalf("result = %+v", res)
	}
	var resume struct{ SessionID string }
	calls := r.calls(t, "session/resume")
	if len(calls) != 1 {
		t.Fatalf("resumes = %d", len(calls))
	}
	json.Unmarshal(calls[0].Params, &resume)
	if resume.SessionID != "fake-session-1" {
		t.Fatalf("resume = %+v", resume)
	}
	select {
	case <-h.Done():
		t.Fatal("Done closed although the process restarted")
	default:
	}
}

func TestPermissionWithNoTurnIsAllowedAsT3Does(t *testing.T) {
	_, r, _ := open(t, map[string]string{"FAKE_AGY_STARTUP_PERMISSION": "1"}, nil)
	eventually(t, "the allowed answer", func() bool {
		for _, m := range r.messages(t) {
			if string(m.ID) == `"perm-startup"` {
				return strings.Contains(string(m.Result), `"selected"`)
			}
		}
		return false
	})
}

func TestDeliveredSurvivesACrashAndTheInputMayArriveTwice(t *testing.T) {
	h, _, _ := open(t, nil, nil)
	if _, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "crash")}, func() {}); err == nil {
		t.Fatal("a process that died mid-turn must fail the start")
	}
	if ok, _ := h.Delivered(context.Background(), 1); !ok {
		t.Fatal("the input was sent, so the ledger has it")
	}
	// Recover: the turn had not finished, so it is not settled and the runner calls Continue.
	if _, settled, err := h.Recover("t1", []contract.TurnInput{input(1, "crash")}); err != nil || settled {
		t.Fatalf("recover settled an unfinished turn: %v", err)
	}
	if _, settled, _ := h.Recover("t2", []contract.TurnInput{input(7, "x")}); settled {
		t.Fatal("an input the mirror lacks is not settled")
	}
	// The engine sends the same input again.
	if res := start(t, h, "t1", input(1, "say again")); res.Final != "again" {
		t.Fatalf("result = %+v", res)
	}
	data, _ := os.ReadFile(h.SessionFile())
	if n := strings.Count(string(data), `"deliveryId":"1"`); n != 2 {
		t.Fatalf("delivery 1 recorded %d times", n)
	}
	if res, settled, err := h.Recover("t1", []contract.TurnInput{input(1, "x")}); err != nil || !settled || res.Final != "again" {
		t.Fatalf("a finished turn is settled: %+v %v %v", res, settled, err)
	}
}

func TestContinueResumesAfterARestartMidTurn(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	if _, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "crash")}, func() {}); err == nil {
		t.Fatal("the crash must fail the start")
	}
	res, err := h.Continue(context.Background(), "t1", func() {})
	if err != nil || res.Outcome != contract.TurnCompleted || res.Final != "ok" {
		t.Fatalf("continue = %+v %v", res, err)
	}
	if len(r.calls(t, "session/resume")) != 1 {
		t.Fatal("the new process did not resume the session")
	}
	prompts := r.prompts(t)
	if last := prompts[len(prompts)-1]; !strings.Contains(strings.Join(last, "\n"), "Continue where you left off.") {
		t.Fatalf("prompts = %q", prompts)
	}
	data, _ := os.ReadFile(h.SessionFile())
	if !strings.Contains(string(data), "Continue where you left off.") {
		t.Fatal("the continue prompt is not in the ledger")
	}
}

func TestQuotaFailsTheTurnWithoutRetry(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	res, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "quota")}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contract.TurnFailed || res.Cause != contract.CauseModelFailed || res.ModelRetryAfterSeconds != 282 || !strings.Contains(res.Error, "exhausted") {
		t.Fatalf("result = %+v", res)
	}
	if len(r.prompts(t)) != 1 {
		t.Fatal("the harness retried a failed prompt")
	}
}

func TestRefusalAndOutputLimit(t *testing.T) {
	h, _, _ := open(t, nil, nil)
	res := start(t, h, "t1", input(1, "refusal"))
	if res.Outcome != contract.TurnFailed || res.Cause != contract.CauseHarnessFailed {
		t.Fatalf("refusal = %+v", res)
	}
	res = start(t, h, "t2", input(2, "maxtokens"))
	if res.Outcome != contract.TurnFailed || res.Cause != contract.CauseOutputLimit {
		t.Fatalf("max_tokens = %+v", res)
	}
}

func TestGoogleErrorsFailTheTurnLikeT3(t *testing.T) {
	// No ban-specific handling (T3 has none): Google's own message, and a cause the engine does not retry.
	for text, want := range map[string]string{"forbidden": "The caller does not have permission", "tos": "TOS_VIOLATION", "validation": "Verify your Google account, then retry this turn. https://accounts.google.com/signin/continue?token=x"} {
		h, r, _ := open(t, nil, nil)
		res, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, text)}, func() {})
		if err != nil || res.Outcome != contract.TurnFailed || res.Cause != contract.CauseHarnessFailed || !strings.Contains(res.Error, want) || res.ModelRetryAfterSeconds != 0 {
			t.Fatalf("%s: %+v %v", text, res, err)
		}
		if len(r.prompts(t)) != 1 {
			t.Fatal("retried")
		}
	}
	// A 5xx fails the turn with no engine retry: T3 marks it retryable for the user and never retries itself.
	h, _, _ := open(t, nil, nil)
	res, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "fivexx")}, func() {})
	if err != nil || res.Cause != contract.CauseHarnessFailed || res.Error != "backend unavailable" {
		t.Fatalf("5xx: %+v %v", res, err)
	}
}

func TestPromptRPCErrorIsAnErrorAndNotRetried(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	if _, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "rpcerror")}, func() {}); err == nil || !strings.Contains(err.Error(), "failed (code") || strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if len(r.prompts(t)) != 1 {
		t.Fatal("retried")
	}
}

func TestSignInRequired(t *testing.T) {
	t.Setenv(fakeagent.EnvVar, "1")
	exe, _ := os.Executable()
	for env, want := range map[string]error{"FAKE_AGY_AUTH_FAIL": ErrSignInRequired, "FAKE_AGY_AUTH_SUBSCRIPTION": ErrSubscriptionRequired} {
		t.Setenv(env, "1")
		_, err := Open(context.Background(), Config{Binary: exe, GeminiDir: t.TempDir(), Dir: t.TempDir(), SessionDir: t.TempDir()})
		if !errors.Is(err, want) {
			t.Fatalf("%s: err = %v", env, err)
		}
		t.Setenv(env, "")
	}
}

func TestModelTransferAndRefusal(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	res, err := h.TransferModel(context.Background(), "t1", contract.ModelChoice{ID: "gemini-4-neon", Thinking: contract.ThinkingHigh}, 0, "claude guidance")
	if err != nil || res.Path != "switched" {
		t.Fatalf("transfer = %+v %v", res, err)
	}
	calls := r.calls(t, "session/set_config_option")
	if last := string(calls[len(calls)-2].Params); !strings.Contains(last, "gemini-4-neon") {
		t.Fatalf("calls = %v", calls)
	}
	if _, err := h.TransferModel(context.Background(), "t1", contract.ModelChoice{ID: "no-such-model"}, 0, ""); !errors.Is(err, harness.ErrRefused) {
		t.Fatalf("unlisted model = %v", err)
	}
	// The refused transfer left the model alone, and the guidance rides the next prompt as a section.
	start(t, h, "t2", input(1, "say hi"))
	got := r.prompts(t)[0]
	if len(got) != 3 || !strings.Contains(got[0], "claude guidance") {
		t.Fatalf("prompt = %v", got)
	}
}

func TestOpenRefusesAnUnlistedModel(t *testing.T) {
	r := &rig{}
	_ = r
	t.Setenv(fakeagent.EnvVar, "1")
	exe, _ := os.Executable()
	_, err := Open(context.Background(), Config{Binary: exe, GeminiDir: t.TempDir(), Dir: t.TempDir(), SessionDir: t.TempDir(), Model: contract.ModelChoice{ID: "nope"}})
	if !errors.Is(err, harness.ErrRefused) {
		t.Fatalf("err = %v", err)
	}
}

func TestPromptCarriesInstructionsOnceAndContextWhenChanged(t *testing.T) {
	h, r, _ := open(t, nil, func(c *Config) {
		c.ProjectInstructions = []string{`<project_instructions path="/a">rule a</project_instructions>`}
	})
	ctx := context.Background()
	h.SetPromptSections(ctx, []contract.ContextSection{{Key: "dyna_goal", Text: "ship it"}})
	start(t, h, "t1", input(1, "say a"))
	start(t, h, "t2", input(2, "say b"))
	h.SetPromptSections(ctx, []contract.ContextSection{{Key: "dyna_goal", Text: "ship it"}, {Key: "dyna_time", Text: "noon"}})
	start(t, h, "t3", input(3, "say c"))
	got := r.prompts(t)
	// first: context, wrapped request, runtime info
	if len(got[0]) != 3 || got[0][0] != "<external_dyna_goal>ship it</external_dyna_goal>" ||
		!strings.Contains(got[0][1], "be Dyna") || !strings.Contains(got[0][1], "rule a") || !strings.Contains(got[0][1], "<user_request>\nsay a") ||
		!strings.HasPrefix(got[0][2], "<runtime_info>") {
		t.Fatalf("first prompt = %q", got[0])
	}
	if len(got[1]) != 1 || got[1][0] != "say b" {
		t.Fatalf("second prompt = %q", got[1])
	}
	if len(got[2]) != 2 || got[2][0] != "<external_dyna_time>noon</external_dyna_time>" || got[2][1] != "say c" {
		t.Fatalf("third prompt = %q", got[2])
	}
	// agy is given nothing in its system prompt, and a slash command carries nothing.
	if strings.Contains(string(r.calls(t, "session/new")[0].Params), "systemPrompt") {
		t.Fatal("session/new appended to the system prompt")
	}
}

func TestCompactionResendsWhatAgyWasTold(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	start(t, h, "t1", input(1, "say a"))
	if c, err := h.Compact(context.Background(), 1); err != nil || !c.Compacted {
		t.Fatalf("compact %+v %v", c, err)
	}
	start(t, h, "t2", input(2, "say b"))
	got := r.prompts(t)
	if got[1][0] != "/compact" || len(got[1]) != 1 {
		t.Fatalf("a slash command carries nothing: %q", got[1])
	}
	if len(got[2]) != 2 || !strings.Contains(got[2][0], "<user_request>\nsay b") {
		t.Fatalf("after compaction: %q", got[2])
	}
}

func TestNewProcessIsToldAgain(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	start(t, h, "t1", input(1, "say a"))
	h.mu.Lock()
	p := h.p
	h.mu.Unlock()
	p.kill()
	c := h.cfg
	c.New = false
	start(t, h, "t2", input(2, "say b"))
	if got := r.prompts(t); len(got) != 2 || !strings.Contains(got[1][0], "<user_request>") {
		t.Fatalf("prompts = %q", got)
	}
	_ = c
}

func TestErrorsComeFromTheSessionLogNotTheReply(t *testing.T) {
	h, _, _ := open(t, nil, nil)
	res := start(t, h, "t1", input(1, "quotatext"))
	if res.Outcome != contract.TurnCompleted || res.Final != "Error 429, Message: quota, Status: RESOURCE_EXHAUSTED" {
		t.Fatalf("a reply that quotes an error is no failure: %+v", res)
	}
	res = start(t, h, "t2", input(2, "odd"))
	if res.Outcome != contract.TurnFailed || res.Cause != contract.CauseHarnessFailed || res.Error != "Antigravity stopped this turn because of an error." {
		t.Fatalf("odd error step: %+v", res)
	}
}

func TestNotFoundIsRetriedOnlyAfterTheModelAnswered(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	if res := start(t, h, "t1", input(1, "notfound")); res.Cause != contract.CauseHarnessFailed {
		t.Fatalf("before: %+v", res)
	}
	// After the model answered, T3 sends "continue" once in the same turn (adapter.ts:10242-10275); the engine retries nothing.
	res := start(t, h, "t2", input(2, "notfoundlate"))
	if res.Outcome != contract.TurnCompleted {
		t.Fatalf("after: %+v", res)
	}
	if p := r.prompts(t); len(p) != 3 || p[2][0] != "continue" {
		t.Fatalf("prompts = %q", p)
	}
}

func TestInvalidThinkingSignatureBreaksTheConversation(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	res := start(t, h, "t1", input(1, "signature"))
	if res.Outcome != contract.TurnFailed || res.Cause != contract.CauseHarnessFailed {
		t.Fatalf("result %+v", res)
	}
	start(t, h, "t2", input(2, "say b"))
	if len(r.calls(t, "session/new")) != 2 {
		t.Fatalf("the next turn opens a new session: %d", len(r.calls(t, "session/new")))
	}
}

func TestStaleSessionUpdateIsIgnored(t *testing.T) {
	h, _, _ := open(t, nil, nil)
	if res := start(t, h, "t1", input(1, "stale")); res.Final != "fresh" {
		t.Fatalf("final = %q", res.Final)
	}
}

func TestSettledTurnIsHeldForABackgroundCommandAndStopCancelsIt(t *testing.T) {
	h, r, _ := open(t, map[string]string{"FAKE_AGY_BG_DELAY": "300ms"}, nil)
	h.watchEvery = 20 * time.Millisecond
	res := start(t, h, "t1", input(1, "bg"))
	if res.Final != "background done" {
		t.Fatalf("the turn ended before the command: %+v", res)
	}
	// A command that never ends is bounded by the hold limit, and the turn's end leaves it running until Stop.
	h2, r2, _ := open(t, map[string]string{"FAKE_AGY_BG_DELAY": "1h"}, nil)
	h2.watchEvery, h2.holdMax = 20*time.Millisecond, 150*time.Millisecond
	res = start(t, h2, "t2", input(1, "bg"))
	if res.Outcome != contract.TurnCompleted || res.LastText != "started" {
		t.Fatalf("held turn: %+v", res)
	}
	if len(r2.calls(t, "session/cancel")) != 0 {
		t.Fatal("the end of the turn cancelled the command")
	}
	if err := h2.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "session/cancel", func() bool { return len(r2.calls(t, "session/cancel")) == 1 })
	_ = r
}

func TestPermissionModeIsPutBackToDefault(t *testing.T) {
	_, r, _ := open(t, map[string]string{"FAKE_AGY_MODE": "yolo"}, nil)
	if len(r.calls(t, "session/set_mode")) != 1 || !strings.Contains(string(r.calls(t, "session/set_mode")[0].Params), `"modeId":"default"`) {
		t.Fatal("no set_mode default")
	}
}

func TestResumeNeedsTheCapability(t *testing.T) {
	_, _, c := open(t, nil, nil)
	t.Setenv("FAKE_AGY_NO_RESUME", "1")
	c.New = false
	if _, err := Open(context.Background(), c); err == nil || !strings.Contains(err.Error(), "does not advertise session/resume") {
		t.Fatalf("err = %v", err)
	}
}

func TestFailedThoughtLevelRestoresTheModel(t *testing.T) {
	h, r, _ := open(t, map[string]string{"FAKE_AGY_FAIL_THOUGHT": "1"}, func(c *Config) { c.Model.Thinking = "" })
	_, err := h.TransferModel(context.Background(), "t", contract.ModelChoice{ID: "gemini-4-neon", Thinking: contract.ThinkingLow}, 0, "")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	calls := r.calls(t, "session/set_config_option")
	last := string(calls[len(calls)-1].Params)
	if !strings.Contains(last, `"value":"gemini-3.8-flash"`) {
		t.Fatalf("the previous model was not put back: %s", last)
	}
}

func TestEnvironmentCAHandling(t *testing.T) {
	if env := environment([]string{"SSL_CERT_DIR=/certs"}, "/gd", "/t"); slices.ContainsFunc(env, func(s string) bool { return strings.HasPrefix(s, "SSL_CERT_FILE=") }) {
		t.Errorf("SSL_CERT_DIR set: %v", env)
	}
	if env := environment([]string{"gemini_api_key=k"}, "/gd", "/t"); slices.Contains(env, "gemini_api_key=k") {
		t.Errorf("env names are matched without case: %v", env)
	}
}

func TestAttachments(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) contract.TurnFile {
		path := filepath.Join(dir, name)
		os.WriteFile(path, data, 0o600)
		return contract.TurnFile{Path: path, Bytes: int64(len(data))}
	}
	p := &process{image: true, embedded: true}
	var total int64
	blocks, err := attachmentBlocks([]contract.TurnFile{write("a.png", []byte("png")), write("b.go", []byte("package x")), write("c.pdf", []byte("pdf")),
		write("d.bin", []byte("\x00\x01")), write("e.mp3", []byte("mp3")), write("f.txt", []byte{0xff, 0xfe, 0x00})}, p, &total)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, b := range blocks {
		kinds = append(kinds, b["type"].(string))
	}
	// the png, the Go file and the pdf; the binary, the unadvertised audio and the invalid text stay path-only
	if !slices.Equal(kinds, []string{"image", "resource", "resource_link"}) {
		t.Fatalf("kinds = %v", kinds)
	}
	if _, err := attachmentBlocks([]contract.TurnFile{write("big.png", make([]byte, maxImageBytes+1))}, p, new(int64)); err == nil {
		t.Fatal("an image over 10 MiB must fail")
	}
	if blocks, _ := attachmentBlocks([]contract.TurnFile{write("a.png", []byte("png"))}, &process{}, new(int64)); len(blocks) != 0 {
		t.Fatal("no image capability: path only")
	}
}

func TestToolSummaryIsStrippedAtAnyDepth(t *testing.T) {
	got := stripSummary(map[string]any{"toolSummary": "x", "a": map[string]any{"toolSummary": "y", "b": []any{map[string]any{"toolSummary": "z", "k": 1}}}})
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "toolSummary") {
		t.Fatal(string(data))
	}
}

func TestCompactOnlyWhenAdvertisedAndOver(t *testing.T) {
	h, _, _ := open(t, nil, nil)
	ctx := context.Background()
	start(t, h, "t1", input(1, "say a")) // context 150
	if c, err := h.Compact(ctx, 1000); err != nil || c.Compacted {
		t.Fatalf("under the size: %+v %v", c, err)
	}
	c, err := h.Compact(ctx, 100)
	if err != nil || !c.Compacted || c.TokensBefore != 150 || c.TokensAfter != 1000 {
		t.Fatalf("compact = %+v %v", c, err)
	}
	h2, _, _ := open(t, map[string]string{"FAKE_AGY_NO_COMPACT": "1"}, nil)
	start(t, h2, "t1", input(1, "say a"))
	if c, err := h2.Compact(ctx, 1); err != nil || c.Compacted {
		t.Fatalf("not advertised: %+v %v", c, err)
	}
}

func TestNewConversationStartsANewSessionAndMirror(t *testing.T) {
	h, r, c := open(t, nil, nil)
	ctx := context.Background()
	if err := h.NewConversation(ctx); err != nil || len(r.calls(t, "session/new")) != 1 {
		t.Fatalf("an empty conversation stays: %v", err)
	}
	start(t, h, "t1", input(1, "say a"))
	if err := h.NewConversation(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.calls(t, "session/new")) != 2 || h.SessionFile() != filepath.Join(c.SessionDir, "agy-2.jsonl") {
		t.Fatalf("file = %s", h.SessionFile())
	}
	if ok, _ := h.Delivered(ctx, 1); ok {
		t.Fatal("the new conversation has no deliveries")
	}
}

func TestSilentTurnIsEnded(t *testing.T) {
	h, _, _ := open(t, nil, nil)
	h.promptInactivity, h.watchEvery = 100*time.Millisecond, 20*time.Millisecond
	_, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "stubborn")}, func() {})
	if !errors.Is(err, errInactive) {
		t.Fatalf("err = %v", err)
	}
}

func TestCloseEndsTheProcessAndDone(t *testing.T) {
	h, _, _ := open(t, nil, nil)
	h.Close()
	select {
	case <-h.Done():
	default:
		t.Fatal("Done is open after Close")
	}
	if _, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "say a")}, func() {}); err == nil {
		t.Fatal("a closed harness started a turn")
	}
}

func TestEnvironmentFollowsT3(t *testing.T) {
	env := environment([]string{"HOME=/h", "PATH=/p", "GEMINI_API_KEY=k", "GOOGLE_API_KEY=k", "BROWSER=b", "TMPDIR=/t", "KEEP=1"}, "/gd", "/tmp/x")
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{"HOME=/h", "PATH=/p", "KEEP=1", "GEMINI_HOME=/gd", "AGY_ACP_FORCE_FILE_STORAGE=1", "AGY_CLI_DISABLE_AUTO_UPDATE=true",
		"PYTHONUNBUFFERED=1", "TMPDIR=/tmp/x", "SSH_CONNECTION=127.0.0.1 0 127.0.0.1 0", "BROWSER=" + browserCommand} {
		if !strings.Contains(joined, "\n"+want+"\n") {
			t.Errorf("missing %s in %v", want, env)
		}
	}
	for _, bad := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "BROWSER=b", "TMPDIR=/t\n"} {
		if strings.Contains(joined, bad) {
			t.Errorf("%s was kept", bad)
		}
	}
	// The host's SSH_CONNECTION wins over the fake one (ASUP:230-234).
	if env := environment([]string{"SSH_CONNECTION=1 2 3 4"}, "/gd", "/tmp/x"); !slices.Contains(env, "SSH_CONNECTION=1 2 3 4") || slices.Contains(env, "SSH_CONNECTION=127.0.0.1 0 127.0.0.1 0") {
		t.Errorf("env = %v", env)
	}
}

func TestClassify(t *testing.T) {
	now := time.Now()
	body := "{\n  \"error\": {\"code\": 403, \"status\": \"PERMISSION_DENIED\", \"details\": [{\"reason\": \"VALIDATION_REQUIRED\", \"domain\": \"cloudcode-pa.googleapis.com\", \"metadata\": {\"validation_url\": \"https://accounts.google.com/signin/continue?a=b\"}}]}}"
	cases := []struct {
		text    string
		cause   contract.FailureCause // empty: not an error
		message string
		retry   time.Duration
	}{
		{"Error 429, Message: Resource exhausted. Resets in 4m42s., Status: RESOURCE_EXHAUSTED", contract.CauseModelFailed, "Resource exhausted. Resets in 4m42s.", 282 * time.Second},
		{"RESOURCE_EXHAUSTED (code 429): too many requests retryDelay:3.5s ]", contract.CauseModelFailed, "too many requests retryDelay:3.5s ]", 4 * time.Second},
		{"Agent error:\nError 429, Message: out [reason:QUOTA_EXHAUSTED quotaResetDelay:1h2m3s ], Status: RESOURCE_EXHAUSTED", contract.CauseModelFailed, "", time.Hour + 3*time.Second + 2*time.Minute},
		{"Error 429, Message: out [quotaResetTimeStamp:" + now.Add(90*time.Second).UTC().Format(time.RFC3339) + " ], Status: RESOURCE_EXHAUSTED", contract.CauseModelFailed, "", 90 * time.Second},
		{"Error 500, Message: backend, Status: INTERNAL", contract.CauseHarnessFailed, "backend", 0},
		// Not retried: Google's own message, shown as it is (no ban-specific handling, as in T3).
		{"Error 403, Message: denied, Status: PERMISSION_DENIED", contract.CauseHarnessFailed, "denied", 0},
		{"Error 403, Message: restricted (TOS_VIOLATION), Status: PERMISSION_DENIED", contract.CauseHarnessFailed, "restricted (TOS_VIOLATION)", 0},
		{"Error 403, Message: SUBSCRIPTION_REQUIRED, Status: PERMISSION_DENIED", contract.CauseHarnessFailed, "SUBSCRIPTION_REQUIRED", 0},
		{"Error 404, Message: no such model, Status: NOT_FOUND", contract.CauseHarnessFailed, "no such model", 0},
		{strings.Replace(body, `"reason"`, `"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason"`, 1), contract.CauseHarnessFailed, "Verify your Google account, then retry this turn. https://accounts.google.com/signin/continue?a=b", 0},
		{"nothing of the shapes", "", "", 0},
		{"TOS_VIOLATION", "", "", 0},
		// A 404 is retried only once the model had answered.
		{"Error 404, Message: gone, Status: NOT_FOUND", contract.CauseHarnessFailed, "gone", 0},
	}
	for _, c := range cases {
		f := classify(c.text, now, false)
		switch {
		case f == nil && c.cause != "":
			t.Errorf("%.60q not classified", c.text)
		case f != nil && c.cause == "":
			t.Errorf("%.60q taken for an error: %+v", c.text, f)
		case f != nil && (f.cause != c.cause || (c.message != "" && f.message != c.message) ||
			time.Duration(f.retryAfter)*time.Second < c.retry-2*time.Second || time.Duration(f.retryAfter)*time.Second > c.retry+2*time.Second):
			t.Errorf("%.60q = %+v", c.text, f)
		}
	}
}

func TestUsageLimitResets(t *testing.T) {
	now := time.Now()
	for text, want := range map[string]struct {
		cause   contract.FailureCause
		seconds int64
	}{
		"Error 429, Message: out [reason:QUOTA_EXHAUSTED quotaResetDelay:72h ], Status: RESOURCE_EXHAUSTED": {contract.CauseModelFailed, 72 * 3600},
		"Error 429, Message: out, Status: RESOURCE_EXHAUSTED":                                               {contract.CauseHarnessFailed, 0}, // no reset: no ladder
		"Error 429, Message: out [quotaResetDelay:1000h ], Status: RESOURCE_EXHAUSTED":                      {contract.CauseHarnessFailed, 0}, // beyond the bound
		"Error 503, Message: down, Status: UNAVAILABLE":                                                     {contract.CauseHarnessFailed, 0},
	} {
		f := classify(text, now, false)
		if f == nil || f.cause != want.cause || f.retryAfter != want.seconds {
			t.Errorf("%q = %+v", text, f)
		}
	}
	if f := classify("Error 429, Message: x [quotaResetDelay:100h], Status: RESOURCE_EXHAUSTED", now, false); f != nil && f.retryAfter > contract.MaxModelQuotaResetSeconds {
		t.Error("over the bound")
	}
}

func TestAFailedTurnIsNeverRecoveredAsCompleted(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	res := start(t, h, "t1", input(1, "quota"))
	if res.Outcome != contract.TurnFailed {
		t.Fatalf("quota = %+v", res)
	}
	// The retry wake reuses the delivery id: whatever text came before the failure, the turn is not finished.
	if _, settled, err := h.Recover("t1", []contract.TurnInput{input(1, "quota")}); err != nil || settled {
		t.Fatalf("recover settled a failed turn: %v %v", settled, err)
	}
	if res, err := h.Continue(context.Background(), "t1", func() {}); err != nil || res.Outcome != contract.TurnCompleted {
		t.Fatalf("continue = %+v %v", res, err)
	}
	prompts := r.prompts(t)
	if last := prompts[len(prompts)-1]; !strings.Contains(strings.Join(last, "\n"), "Continue where you left off.") {
		t.Fatalf("prompts = %q", prompts)
	}
	data, _ := os.ReadFile(h.SessionFile())
	if !strings.Contains(string(data), `"stopReason":"error"`) {
		t.Fatal("the failure is not in the mirror")
	}
}

func TestAStoppedTurnIsRecoveredAsStopped(t *testing.T) {
	h, _, _ := open(t, nil, nil)
	done := make(chan contract.TurnEnded, 1)
	go func() {
		res, _ := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "hold")}, func() {})
		done <- res
	}()
	eventually(t, "the prompt", func() bool { d, _ := h.Delivered(context.Background(), 1); return d })
	if err := h.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.Outcome != contract.TurnStopped {
		t.Fatalf("res = %+v", res)
	}
	if res, settled, _ := h.Recover("t1", []contract.TurnInput{input(1, "hold")}); !settled || res.Outcome != contract.TurnStopped {
		t.Fatalf("recover = %+v %v", res, settled)
	}
}

func TestAbortWithNoTurnCancelsTheSession(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	start(t, h, "t1", input(1, "say hi"))
	for range 2 {
		if err := h.Abort(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "session/cancel", func() bool { return len(r.calls(t, "session/cancel")) == 2 })
}

func TestCompactSendsNoPromptAfterALimit(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	start(t, h, "t1", input(1, "quota"))
	if got, err := h.Compact(context.Background(), -1); err != nil || got.Compacted {
		t.Fatalf("compact = %+v %v", got, err)
	}
	for _, p := range r.prompts(t) {
		if strings.Contains(strings.Join(p, ""), "/compact") {
			t.Fatal("a /compact prompt went to Google after a limit")
		}
	}
}

func TestFailureTextIsRedactedAsT3Does(t *testing.T) {
	got := safeMessage("bad \x01 call https://user:pw@example.com/p?token=abc#f, Bearer abc.def api_key=sk-12345678901234567890 done")
	for _, bad := range []string{"pw", "token=abc", "abc.def", "12345678901234567890", "\x01"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q kept in %q", bad, got)
		}
	}
	if !strings.Contains(got, "https://example.com/p,") {
		t.Errorf("url = %q", got)
	}
	if url, ok := validationRequired(`{"error": {"code": 403, "status": "PERMISSION_DENIED", "details": [{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "VALIDATION_REQUIRED", "domain": "cloudcode-pa.googleapis.com", "metadata": {"validation_url": "https://evil.example/signin/continue?a=b"}}]}}`); !ok || googleVerificationURL.MatchString(url) {
		t.Errorf("a URL that is not Google's was accepted: %q", url)
	}
	if f := classify(`{"error": {"code": 403, "status": "PERMISSION_DENIED", "details": [{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "VALIDATION_REQUIRED", "domain": "cloudcode-pa.googleapis.com", "metadata": {"validation_url": "https://evil.example/signin/continue?a=b"}}]}}`, time.Now(), false); f == nil || strings.Contains(f.message, "evil") {
		t.Errorf("failure = %+v", f)
	}
}

func TestBrowserCommandOpensNothing(t *testing.T) {
	out, err := exec.Command("sh", "-c", strings.Replace(browserCommand, "%s", "https://example.invalid/x", 1)).CombinedOutput()
	if err != nil || len(out) != 0 {
		t.Fatalf("browser command = %q %v", out, err)
	}
}

func TestALaunchStaysTrackedUntilItsChildrenAreIdle(t *testing.T) {
	h, _, _ := open(t, nil, nil)
	h.watchEvery = 20 * time.Millisecond
	brain := filepath.Join(h.cfg.GeminiDir, "antigravity-acp", "brain")
	write := func(session, rel, data string) {
		path := filepath.Join(brain, session, ".system_generated", rel)
		os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("fake-session-1", "subagents/child-1.json", `{"conversationId":"child-1","state":"SUBAGENT_STATE_RUNNING"}`)
	write("child-1", "messages/m.json", `{"sender":"fake-session-1","sourceMetadata":{"tool":{"conversationId":"fake-session-1","toolCall":{"id":"call_9","name":"invoke_subagent"}}}}`)
	done := make(chan contract.TurnEnded, 1)
	go func() {
		res, _ := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "launch")}, func() {})
		done <- res
	}()
	// The launch call completed, but its child works: the turn is held.
	select {
	case res := <-done:
		t.Fatalf("the turn ended with a child at work: %+v", res)
	case <-time.After(500 * time.Millisecond):
	}
	// The child's report reaches the parent's inbox: the turn ends.
	write("fake-session-1", "messages/r.json", `{"sender":"child-1"}`)
	select {
	case res := <-done:
		if res.Outcome != contract.TurnCompleted {
			t.Fatalf("res = %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn was not released by the child's report")
	}
}

func TestActivityWithNoTurnIsMirrored(t *testing.T) {
	h, _, _ := open(t, nil, nil)
	start(t, h, "t1", input(1, "late"))
	eventually(t, "the late reply in the mirror", func() bool {
		data, _ := os.ReadFile(h.SessionFile())
		return strings.Contains(string(data), "late reply")
	})
}

func TestAnOverlappingSteerCarriesOnlyWhatIsNew(t *testing.T) {
	h, _, _ := open(t, nil, func(c *Config) { c.Instructions = "be brief" })
	ctx := context.Background()
	h.SetPromptSections(ctx, []contract.ContextSection{{Key: "dyna_goal", Text: "ship it"}})
	h.mu.Lock()
	p := h.p
	h.mu.Unlock()
	tn := &turn{}
	first, _, err := h.promptBlocks(tn, []contract.TurnInput{input(1, "go")}, p, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(first)), "be brief") || !strings.Contains(string(mustJSON(first)), "dyna_goal") || !strings.Contains(string(mustJSON(first)), "runtime_info") {
		t.Fatalf("first = %s", mustJSON(first))
	}
	// The first prompt is still in flight: the steer repeats nothing it offered, and carries no instructions or runtime info.
	h.SetPromptSections(ctx, []contract.ContextSection{{Key: "dyna_goal", Text: "ship it"}, {Key: "dyna_time", Text: "noon"}})
	steer, _, err := h.promptBlocks(tn, []contract.TurnInput{input(2, "also")}, p, true)
	if err != nil {
		t.Fatal(err)
	}
	got := string(mustJSON(steer))
	if strings.Contains(got, "be brief") || strings.Contains(got, "runtime_info") || strings.Contains(got, "dyna_goal") || !strings.Contains(got, "dyna_time") || !strings.Contains(got, "also") {
		t.Fatalf("steer = %s", got)
	}
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

func answerOf(t *testing.T, r *rig, id string) string {
	t.Helper()
	got := ""
	for _, m := range r.messages(t) {
		if string(m.ID) == `"`+id+`"` && len(m.Result) > 0 {
			got = string(m.Result)
		}
	}
	return got
}

func TestDetachedOutputAfterAFailureDoesNotCompleteTheTurn(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	res := start(t, h, "t1", input(1, "quotalate"))
	if res.Outcome != contract.TurnFailed {
		t.Fatalf("res = %+v", res)
	}
	eventually(t, "the detached reply in the mirror", func() bool {
		data, _ := os.ReadFile(h.SessionFile())
		return strings.Contains(string(data), "detached reply")
	})
	// The retry wake reuses the delivery id: the reply agy streamed on its own is no answer to it.
	if _, settled, err := h.Recover("t1", []contract.TurnInput{input(1, "quotalate")}); err != nil || settled {
		t.Fatalf("recover took detached output for the answer: %v %v", settled, err)
	}
	if res, err := h.Continue(context.Background(), "t1", func() {}); err != nil || res.Outcome != contract.TurnCompleted {
		t.Fatalf("continue = %+v %v", res, err)
	}
	prompts := r.prompts(t)
	if last := prompts[len(prompts)-1]; !strings.Contains(strings.Join(last, "\n"), "Continue where you left off.") {
		t.Fatalf("prompts = %q", prompts)
	}
}

func TestStopQuarantinesWhatAgyDoesOnItsOwn(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	start(t, h, "t1", input(1, "permlate"))
	if err := h.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the cancelled answer", func() bool { return strings.Contains(answerOf(t, r, "perm-late"), `"cancelled"`) })
	// The next turn lifts it.
	start(t, h, "t2", input(2, "permlate"))
	eventually(t, "the allowed answer", func() bool { return strings.Contains(answerOf(t, r, "perm-late"), `"selected"`) })
}

func TestAgysQuestionsAreAnsweredCancelled(t *testing.T) {
	h, r, _ := open(t, nil, nil)
	start(t, h, "t1", input(1, "elicit"))
	for id, want := range map[string]string{
		"elicit-form":   `{"action":"cancel"}`,
		"elicit-legacy": `{"action":{"action":"cancel"}}`,
		"elicit-url":    `{"action":"decline"}`,
		"perm-question": `{"outcome":{"outcome":"cancelled"}}`,
	} {
		if got := answerOf(t, r, id); got != want {
			t.Errorf("%s answered %s, want %s", id, got, want)
		}
	}
	if p := r.prompts(t); !strings.Contains(strings.Join(p[0], "\n"), "Dyna's question tool") {
		t.Errorf("the instructions do not name the question tool: %q", p[0])
	}
}

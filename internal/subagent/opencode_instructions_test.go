package subagent

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/harness/opencodefixture"
	"dyna-agent/internal/profile"
)

// A real server owns the configuration. The run clients have distinct per-turn
// environments, which cannot change that server's ambient instructions.
func TestOpenCodeRealSharedServiceInstructions(t *testing.T) {
	fixture := opencodefixture.New(t, nil, nil)
	p := profile.Profile{Name: "mock", Harness: profile.HarnessOpenCode, Model: "canary/probe", Env: fixture.Env}
	checkOpenCodePromptInstructions(t, p, fixture.Last, true)
}

func checkOpenCodePromptInstructions(t *testing.T, p profile.Profile, capture func() string, history bool) {
	t.Helper()
	e := newTestEnv(t, p)
	if _, err := e.m.goalStore().AddRule(e.room.ID, "owner", "", "ROOM_RULE_CANARY"); err != nil {
		t.Fatal(err)
	}
	info, _ := e.start("FIRST_BRIEF_CANARY", func(so *StartOptions) { so.Goal = "FIRST_GOAL_CANARY" })
	ctx, cancel := context.WithTimeout(e.ctx, 7*time.Second)
	defer cancel()
	wait := func(id string, n int) {
		t.Helper()
		_, turn, err := e.waitTurn(ctx, id, n)
		if err != nil || turn.Status != TurnOK {
			t.Fatalf("turn %d: %+v, %v", n, turn, err)
		}
	}
	wait(info.ID, 1)
	first := capture()
	for _, want := range []string{"FIRST_BRIEF_CANARY", "FIRST_GOAL_CANARY", "ROOM_RULE_CANARY", "Delegate only through Dyna"} {
		if !strings.Contains(first, want) {
			t.Errorf("initial model request lost %s", want)
		}
	}
	if _, err := e.send(info.ID, "CONTINUATION_CANARY", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	wait(info.ID, 2)
	next := capture()
	wantCount := 0
	if history {
		wantCount = 1
	}
	if strings.Count(next, "FIRST_BRIEF_CANARY") != wantCount {
		t.Errorf("continuation repeats or loses frozen instructions: brief count=%d, want %d", strings.Count(next, "FIRST_BRIEF_CANARY"), wantCount)
	}
	if !strings.Contains(next, "CONTINUATION_CANARY") {
		t.Error("continuation request lost its task")
	}
	second, _ := e.start("SECOND_BRIEF_CANARY", func(so *StartOptions) { so.Goal = "SECOND_GOAL_CANARY" })
	wait(second.ID, 1)
	other := capture()
	for _, want := range []string{"SECOND_BRIEF_CANARY", "SECOND_GOAL_CANARY", "ROOM_RULE_CANARY"} {
		if !strings.Contains(other, want) {
			t.Errorf("second session lost %s", want)
		}
	}
	if strings.Contains(other, "FIRST_BRIEF_CANARY") || strings.Contains(other, "FIRST_GOAL_CANARY") {
		t.Error("shared service leaked first session instructions into second session")
	}
}

func TestOpenCodeRealServiceCompactsOnlyDueSession(t *testing.T) {
	fixture := opencodefixture.New(t, map[string]any{"compaction": map[string]any{"keep": map[string]any{"tokens": 0}}}, func(body map[string]any) string {
		messages, _ := body["messages"].([]any)
		if len(messages) > 0 {
			last, _ := messages[len(messages)-1].(map[string]any)
			text, _ := last["content"].(string)
			if strings.HasPrefix(text, "You MUST summarize the conversation above") {
				headings := regexp.MustCompile(`(?m)^#{2,3} [^\n]+`).FindAllString(text, -1)
				var summary strings.Builder
				for _, heading := range headings {
					summary.WriteString(heading + "\n- COMPACT_SUMMARY_CANARY\n\n")
				}
				return opencodefixture.Text(summary.String())
			}
		}
		return ""
	})
	p := profile.Profile{Name: "mock", Harness: profile.HarnessOpenCode, Model: "canary/probe", Env: fixture.Env, Compact: &profile.Compact{Tokens: 100, Windows: map[string]int64{"canary/probe": 200000}}}
	e := newTestEnv(t, p)
	ctx, cancel := context.WithTimeout(e.ctx, 7*time.Second)
	defer cancel()
	wait := func(id string, n int) {
		t.Helper()
		_, turn, err := e.waitTurn(ctx, id, n)
		if err != nil || turn.Status != TurnOK {
			t.Fatalf("turn %d: %+v, %v", n, turn, err)
		}
	}
	first, _ := e.start("COMPACT_FIRST_SESSION")
	wait(first.ID, 1)
	if _, err := e.send(first.ID, "COMPACT_CONTINUATION", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	wait(first.ID, 2)
	if !strings.Contains(fixture.Last(), "COMPACT_SUMMARY_CANARY") {
		events, _ := e.m.Store.Events(first.ID)
		t.Errorf("due session did not compact before continuation; events=%v", events)
	}
	for _, want := range []string{"Delegate only through Dyna", "COMPACT_FIRST_SESSION"} {
		if !strings.Contains(fixture.Last(), want) {
			t.Errorf("immediate post-compact turn lost %s", want)
		}
	}
	p.Compact.Tokens = 1000
	if _, err := e.send(first.ID, "ORDINARY_POST_COMPACT_TURN", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	wait(first.ID, 3)
	if count := strings.Count(fixture.Last(), "[DYNA SESSION]"); count != 1 {
		t.Errorf("unchanged post-compact turn repeats the instruction snapshot: %d copies", count)
	}
	second, _ := e.start("COMPACT_SECOND_SESSION")
	wait(second.ID, 1)
	if strings.Contains(fixture.Last(), "COMPACT_SUMMARY_CANARY") {
		t.Error("compaction changed another session")
	}
}

func TestOpenCodeRealServiceModelAndShellEnvironmentStayPerSession(t *testing.T) {
	models := map[string]any{
		"probe":     map[string]any{"name": "Probe", "variants": []any{map[string]any{"id": "careful", "body": map[string]any{"audit_variant": "VARIANT_CANARY", "temperature": 0.23}}}},
		"alternate": map[string]any{"name": "Alternate"},
	}
	fixture := opencodefixture.New(t, map[string]any{"providers": map[string]any{"canary": map[string]any{"models": models}}}, func(body map[string]any) string {
		tools, _ := body["tools"].([]any)
		if len(tools) == 0 {
			return ""
		}
		messages, _ := body["messages"].([]any)
		for _, value := range messages {
			message, _ := value.(map[string]any)
			if message["role"] == "tool" {
				return ""
			}
		}
		return opencodefixture.Tool("shell", map[string]any{"command": "printf %s \"$OC_SESSION_CANARY\"", "description": "Read session canary"})
	})
	firstEnv := map[string]string{}
	for k, v := range fixture.Env {
		firstEnv[k] = v
	}
	firstEnv["OC_SESSION_CANARY"] = "FIRST_ENV_CANARY"
	secondEnv := map[string]string{}
	for k, v := range fixture.Env {
		secondEnv[k] = v
	}
	secondEnv["OC_SESSION_CANARY"] = "SECOND_ENV_CANARY"
	first := profile.Profile{Name: "mock", Harness: profile.HarnessOpenCode, Model: "canary/probe#careful", ExtraArgs: []string{"--auto"}, Env: firstEnv}
	second := profile.Profile{Name: "other", Harness: profile.HarnessOpenCode, Model: "canary/alternate", ExtraArgs: []string{"--auto"}, Env: secondEnv}
	e := newTestEnv(t, first, second)
	ctx, cancel := context.WithTimeout(e.ctx, 7*time.Second)
	defer cancel()
	wait := func(id string) {
		t.Helper()
		_, turn, err := e.waitTurn(ctx, id, 1)
		if err != nil || turn.Status != TurnOK {
			t.Fatalf("model/env turn: %+v, %v", turn, err)
		}
	}
	a, _ := e.start("ENV_FIRST_TASK")
	wait(a.ID)
	raw := fixture.Last()
	for _, want := range []string{`"model":"probe"`, `"audit_variant":"VARIANT_CANARY"`, `"temperature":0.23`, "FIRST_ENV_CANARY"} {
		if !strings.Contains(raw, want) {
			t.Errorf("first session model request lost %s", want)
		}
	}
	b, _ := e.start("ENV_SECOND_TASK", func(so *StartOptions) { so.Profile = "other" })
	wait(b.ID)
	raw = fixture.Last()
	for _, want := range []string{`"model":"alternate"`, "SECOND_ENV_CANARY"} {
		if !strings.Contains(raw, want) {
			t.Errorf("second session model request lost %s", want)
		}
	}
	if strings.Contains(raw, "VARIANT_CANARY") || strings.Contains(raw, "FIRST_ENV_CANARY") {
		t.Error("shared service leaked model options or shell environment across sessions")
	}
}

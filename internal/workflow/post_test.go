package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

func postTestStore() *profile.Store {
	return &profile.Store{Profiles: []profile.Profile{{
		Name: "terra", Harness: profile.HarnessMock, Default: true,
		Taste: 5, Intelligence: 5, Cost: 10,
	}}}
}

func TestWorkflowPostUsesPoster(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-post")
	run, err := runstore.Create("post test", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })

	type call struct {
		run      string
		mentions []string
		text     string
	}
	var calls []call
	result, err := executeTest(context.Background(), Options{
		ScriptSrc: `const a = post("@parent", "Morning brief"); const b = post(["@decoder", "@parser"], "Both"); return [a, b];`,
		Store:     postTestStore(), Run: run, WorkDir: t.TempDir(),
		Poster: func(_ context.Context, runID string, mentions []string, text string) (PostResult, error) {
			calls = append(calls, call{runID, mentions, text})
			return PostResult{ID: "msg_123", Deliveries: []map[string]any{{"to": "r", "delivery": "room"}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["id"] != "msg_123" || len(got[0]["deliveries"].([]any)) != 1 {
		t.Fatalf("post result = %#v", got)
	}
	want := []call{
		{"wf_engine-post", []string{"@parent"}, "Morning brief"},
		{"wf_engine-post", []string{"@decoder", "@parser"}, "Both"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("poster calls = %#v", calls)
	}
}

func TestWorkflowPostDataFormsAppendFencedJSON(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-post-data")
	run, err := runstore.Create("post data", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })

	var texts []string
	_, err = executeTest(context.Background(), Options{
		ScriptSrc: `post("@a", "Land it", {unit: "CH", head: "abc"}); post("@a", {text: "Again", data: [1, 2]}); post("@a", {text: "Plain"}); post("@a", "Null", null);`,
		Store:     postTestStore(), Run: run, WorkDir: t.TempDir(),
		Poster: func(_ context.Context, _ string, _ []string, text string) (PostResult, error) {
			texts = append(texts, text)
			return PostResult{ID: "msg_1"}, nil
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	want := []string{
		"Land it\n\n```json\n{\n  \"unit\": \"CH\",\n  \"head\": \"abc\"\n}\n```",
		"Again\n\n```json\n[\n  1,\n  2\n]\n```",
		"Plain",
		"Null",
	}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("texts = %q", texts)
	}
	if _, err := executeTest(context.Background(), Options{
		ScriptSrc: `post("@a", "x", "not an object")`, Store: postTestStore(), Run: run, WorkDir: t.TempDir(),
		Poster: func(context.Context, string, []string, string) (PostResult, error) { return PostResult{ID: "m"}, nil },
	}); err == nil || !strings.Contains(err.Error(), "object or an array") {
		t.Fatalf("string data error = %v", err)
	}
}

func TestWorkflowPostValidationAndUnavailable(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_engine-post-invalid")
	run, err := runstore.Create("post invalid", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run.Finish("ok", "", nil) })

	for name, script := range map[string]string{
		"unavailable":  `return post("@root", "hello");`,
		"bad address":  `return post(42, "hello");`,
		"bare address": `return post("global", "hello");`,
		"empty text":   `return post("@root", "  ");`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := executeTest(context.Background(), Options{
				ScriptSrc: script, Store: postTestStore(), Run: run, WorkDir: t.TempDir(),
			})
			if err == nil || (!strings.Contains(err.Error(), "unavailable") && !strings.Contains(err.Error(), "non-empty string") && !strings.Contains(err.Error(), "@mention") && !strings.Contains(err.Error(), "start with @")) {
				t.Fatalf("Execute() error = %v", err)
			}
		})
	}
	result, err := executeTest(context.Background(), Options{
		ScriptSrc: `return [typeof mail, typeof msg].join(",");`, Store: postTestStore(), Run: run, WorkDir: t.TempDir(),
	})
	if err != nil || result != `"undefined,undefined"` {
		t.Fatalf("mail and msg globals should be retired, result = %q err = %v", result, err)
	}
}

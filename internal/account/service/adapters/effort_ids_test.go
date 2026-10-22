package adapters

import (
	"reflect"
	"testing"
)

// IDs and names are copied from the real Cursor and Antigravity catalogs.
func TestGroupEffortIDs(t *testing.T) {
	in := []Model{
		{ID: "auto", Name: "Auto (default)"},
		{ID: "claude-fable-5-high", Name: "Claude Fable 5 1M (NO ZDR)"},
		{ID: "claude-fable-5-xhigh", Name: "Claude Fable 5 1M Extra High (NO ZDR)"},
		{ID: "claude-fable-5-thinking-low", Name: "Claude Fable 5 1M Low Thinking (NO ZDR)"},
		{ID: "claude-fable-5-low", Name: "Claude Fable 5 1M Low (NO ZDR)"},
		{ID: "claude-fable-5-thinking-high", Name: "Claude Fable 5 1M Thinking (NO ZDR)"},
		{ID: "claude-opus-4-7-high-fast", Name: "Opus 4.7 High Fast"},
		{ID: "claude-opus-4-7-low-fast", Name: "Opus 4.7 Low Fast"},
		{ID: "gemini-4-argon-high", Name: "Gemini 4 Argon (High)"},
		{ID: "gemini-4-argon-medium", Name: "Gemini 4 Argon (Medium)"},
		{ID: "gpt-oss-120b-medium", Name: "GPT-OSS 120B (Medium)"},
	}
	out := groupEffortIDs(in)
	ids := []string{}
	for _, m := range out {
		ids = append(ids, m.ID)
	}
	want := []string{"auto", "claude-fable-5-high", "claude-fable-5-thinking-high", "claude-opus-4-7-high-fast", "gemini-4-argon-medium", "gpt-oss-120b-medium"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %q, want %q", ids, want)
	}
	fable := out[1]
	wantEfforts := []Effort{{ID: "low", Model: "claude-fable-5-low"}, {ID: "high", Default: true, Model: "claude-fable-5-high"}, {ID: "xhigh", Model: "claude-fable-5-xhigh"}}
	if fable.Name != "Claude Fable 5 1M (NO ZDR)" || !reflect.DeepEqual(fable.Efforts, wantEfforts) {
		t.Fatalf("fable = %+v", fable)
	}
	if argon := out[4]; argon.Name != "Gemini 4 Argon" || !argon.Efforts[0].Default || argon.Efforts[0].ID != "medium" {
		t.Fatalf("argon = %+v", argon)
	}
	if out[5].Efforts != nil {
		t.Fatalf("a lone ID was grouped: %+v", out[5])
	}
	dup := groupEffortIDs([]Model{{ID: "a-high-x", Name: "A"}, {ID: "a-x-high", Name: "B"}})
	if len(dup) != 2 {
		t.Fatalf("IDs with one level were merged: %+v", dup)
	}
}

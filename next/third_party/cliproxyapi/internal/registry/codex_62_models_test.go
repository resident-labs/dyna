package registry

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestEmbeddedCodex62Models(t *testing.T) {
	var catalogs map[string][]struct {
		ID            string   `json:"id"`
		DisplayName   string   `json:"display_name"`
		Created       int64    `json:"created"`
		ContextLength int      `json:"context_length"`
		Input         []string `json:"supportedInputModalities"`
		Thinking      struct {
			Levels []string `json:"levels"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(embeddedModelsJSON, &catalogs); err != nil {
		t.Fatal(err)
	}
	for _, tier := range []string{"codex-team", "codex-plus", "codex-pro", "codex-free"} {
		for _, model := range []struct {
			id, name      string
			contextLength int
		}{
			{"gpt-6.2-sol", "GPT 6.2 Sol", 500000},
			{"gpt-6.2-astra", "GPT 6.2 Astra", 272000},
		} {
			count := 0
			for _, got := range catalogs[tier] {
				if got.ID != model.id {
					continue
				}
				count++
				if got.DisplayName != model.name || got.Created != 1794942000 || got.ContextLength != model.contextLength || !slices.Contains(got.Input, "image") || !slices.Equal(got.Thinking.Levels, []string{"low", "medium", "high", "xhigh", "max"}) {
					t.Errorf("%s: unexpected metadata: %+v", tier, got)
				}
			}
			want := 1
			if tier == "codex-free" {
				want = 0
			}
			if count != want {
				t.Errorf("%s: %s count = %d, want %d", tier, model.id, count, want)
			}
		}
	}
	if err := ValidateCodexClientModelsJSON(embeddedCodexClientModelsJSON); err != nil {
		t.Fatal(err)
	}
	var client struct {
		Models []struct {
			Slug             string `json:"slug"`
			BaseInstructions string `json:"base_instructions"`
		} `json:"models"`
	}
	if err := json.Unmarshal(embeddedCodexClientModelsJSON, &client); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		count := 0
		for _, model := range client.Models {
			if model.Slug == id && model.BaseInstructions != "" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("%s client catalog count with instructions = %d, want 1", id, count)
		}
	}
}

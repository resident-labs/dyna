package service

import (
	"context"
	"testing"

	"dyna-agent/internal/account/service/adapters"
)

func TestCatalogEnrichesOnlyExactDiscoveredRoutes(t *testing.T) {
	s := New(Options{Models: func(context.Context, string, string, []string) ([]adapters.Model, error) {
		return []adapters.Model{{ID: "opencode/goose-free"}, {ID: "opencode-go/kimi-k3"}}, nil
	}})
	s.installed = func() []Installation { return []Installation{{Harness: "pi", Executable: "/pi"}} }
	c, err := s.ProfileModels(context.Background(), "pi", false)
	if err != nil || len(c.Models) != 2 {
		t.Fatalf("%+v %v", c, err)
	}
	for _, m := range c.Models {
		if m.ID == "opencode-go/kimi-k3" && (m.Category != "go" || len(m.Limits) == 0) {
			t.Fatalf("exact catalog metadata missing: %+v", m)
		}
		if m.ID == "opencode/goose-free" && m.Context != 0 {
			t.Fatal("fabricated capabilities")
		}
	}
}

func TestCatalogCacheFollowsExecutable(t *testing.T) {
	executable := "/first/oc"
	calls := 0
	s := New(Options{Models: func(_ context.Context, h, path string, _ []string) ([]adapters.Model, error) {
		calls++
		return []adapters.Model{{ID: path}}, nil
	}})
	s.installed = func() []Installation { return []Installation{{Harness: "pi", Executable: executable}} }
	first, _ := s.ProfileModels(context.Background(), "pi", false)
	executable = "/second/oc"
	second, _ := s.ProfileModels(context.Background(), "pi", false)
	if calls != 2 || first.Models[0].ID == second.Models[0].ID {
		t.Fatalf("old executable catalog reused: calls=%d", calls)
	}
}

func TestPublisherClassificationDoesNotInventUnknownFamilies(t *testing.T) {
	for _, row := range []struct{ provider, id, want string }{{"", "claude-opus-high", "Anthropic"}, {"", "gpt-model-high", "OpenAI"}, {"", "gemini-high", "Google"}, {"opencode", "opencode/big-pickle", ""}, {"vendor", "opaque-model", ""}} {
		if got := modelPublisher(row.id); got != row.want {
			t.Fatalf("publisher for %s = %q, want %q", row.id, got, row.want)
		}
	}
}

// Another active account gets its own catalog, even with the same executable.
func TestCatalogCacheFollowsActiveAccount(t *testing.T) {
	account := "first"
	calls := 0
	s := New(Options{Models: func(context.Context, string, string, []string) ([]adapters.Model, error) {
		calls++
		return []adapters.Model{{ID: account + "-model"}}, nil
	}})
	s.installed = func() []Installation {
		return []Installation{{Harness: "pi", Executable: "/pi", AccountID: account}}
	}
	first, _ := s.ProfileModels(context.Background(), "pi", false)
	account = "second"
	second, _ := s.ProfileModels(context.Background(), "pi", false)
	if calls != 2 || first.Models[0].ID == second.Models[0].ID {
		t.Fatalf("an account switch reused the old catalog: calls=%d", calls)
	}
}

func TestCatalogCarriesTheCompactRule(t *testing.T) {
	s := New(Options{Models: func(context.Context, string, string, []string) ([]adapters.Model, error) { return nil, nil }})
	s.installed = func() []Installation { return nil }
	// The rule shows even when the harness is not installed.
	if c, _ := s.ProfileModels(context.Background(), "pi", false); c.Compact.Trigger || c.Compact.Min != 1 || c.Compact.Reason != "" {
		t.Fatalf("pi rule = %+v", c.Compact)
	}
	if c, _ := s.ProfileModels(context.Background(), "mock", false); c.Compact.Reason == "" {
		t.Fatalf("mock rule = %+v", c.Compact)
	}
}

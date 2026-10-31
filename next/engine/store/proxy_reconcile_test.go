package store

import (
	"context"
	"testing"
)

func TestProxyRestartPrunesOnlyUnboundSupportedCredentials(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, auth := range []ProxyAuth{
		{ID: "bound", Provider: "claude", Record: []byte("{}")},
		{ID: "orphan", Provider: "codex", Record: []byte("{}")},
		{ID: "dyna-key-0123456789abcdef0123456789abcdef", Provider: "openai-compatible-zai", Record: []byte("{}")},
		{ID: "unsupported", Provider: "other", Record: []byte("{}")},
	} {
		if err := s.SaveProxyAuth(ctx, auth, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.BindAccount(ctx, "account", "bound", "anthropic", "Invented"); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneUnboundProxyAuths(ctx); err != nil {
		t.Fatal(err)
	}
	auths, err := s.ListProxyAuths(ctx)
	if err != nil || len(auths) != 2 || auths[0].ID != "bound" || auths[1].ID != "unsupported" {
		t.Fatal("restart reconciliation lost or retained the wrong credential")
	}
}

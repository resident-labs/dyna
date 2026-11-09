package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

// pinProfiles gives "test" a Claude model and "other" a Codex one, and "same" a second
// Claude model.
type pinProfiles struct{ fakeProfiles }

func (pinProfiles) Model(name contract.ProfileName) (contract.ModelChoice, []string, error) {
	id := map[contract.ProfileName]string{"test": "claude-model", "same": "claude-other", "other": "codex-model"}[name]
	return contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: id}, []string{id}, nil
}

type pinProxy struct {
	mu          sync.Mutex
	credentials []map[string]any
	places      []map[string]any
	down        bool
}

func (p *pinProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch r.URL.Path {
	case "/dyna/credentials":
		if p.down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"credentials": p.credentials})
	case "/v8/management/routing/status":
		if len(p.places) == 0 {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"providers": map[string]any{"claude": p.places}})
	default:
		http.NotFound(w, r)
	}
}

func (p *pinProxy) set(id string, field string, value any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, credential := range p.credentials {
		if credential["id"] == id {
			credential[field] = value
		}
	}
}

func (p *pinProxy) place(index string, rank int, spent bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.places = append(p.places, map[string]any{"auth_index": index, "rank": rank, "spent": spent})
}

func pinCredential(id, provider string, models ...string) map[string]any {
	return map[string]any{"id": id, "auth_index": "index-" + id, "provider": provider, "state": "connected", "models": models}
}

func pinHarness(t *testing.T) (*AccountActions, *pinProxy, contract.SessionID, context.Context) {
	t.Helper()
	s, _, session, ctx := setup(t)
	s.profiles = pinProfiles{}
	for _, account := range []struct {
		id       contract.AccountID
		proxy    string
		provider contract.AccountProvider
		label    string
	}{
		{"a", "proxy-a", contract.ProviderAnthropic, "First"},
		{"b", "proxy-b", contract.ProviderAnthropic, "Second"},
		{"c", "proxy-c", contract.ProviderOpenAI, "Codex"},
	} {
		if err := s.store.SaveProxyAuth(ctx, store.ProxyAuth{ID: account.proxy, Provider: "invented", Record: []byte("{}")}, true); err != nil {
			t.Fatal(err)
		}
		if err := s.store.BindAccount(ctx, account.id, account.proxy, account.provider, account.label); err != nil {
			t.Fatal(err)
		}
	}
	fake := &pinProxy{credentials: []map[string]any{
		pinCredential("proxy-a", "claude", "claude-model", "claude-other"),
		pinCredential("proxy-b", "claude", "claude-model", "claude-other"),
		pinCredential("proxy-c", "codex", "codex-model"),
	}}
	// The proxy's socket path has to stay short, so it cannot live under the test's name.
	dir, err := os.MkdirTemp("/tmp", "pin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	proxy, err := model.NewProxy(dir, filepath.Join(dir, "auth"), "invented-management-key")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: fake}
	go func() { _ = server.Serve(proxy.Listener) }()
	t.Cleanup(func() { _ = server.Close() })
	actions := NewAccountActions(s, proxy.Connections("invented-management-key", s.store, time.Now), proxy.Quotas("invented-management-key", s.store, time.Now))
	return actions, fake, session, ctx
}

var pinOwner = contract.Principal{Kind: contract.PrincipalOwner}

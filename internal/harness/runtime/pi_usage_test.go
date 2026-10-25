package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

func piUsageCredential(t *testing.T, entryID, key string) native.Captured {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"auth.json": map[string]any{entryID: map[string]any{"type": "api_key", "key": key}}})
	if err != nil {
		t.Fatal(err)
	}
	return native.Captured{Identity: "pi:" + entryID, Credential: store.Credential{Schema: "pi/native-files@1", Payload: payload}}
}

func TestSavedPiAccountReadsItsOwnAuthFile(t *testing.T) {
	accounts := &Accounts{Root: t.TempDir()}
	account, err := accounts.CreateProvider("pi", store.ProviderDeepSeek, store.KindAPIKey, "test")
	if err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(account.Home, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"deepseek":{"type":"api_key","key":"fixture-old"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Capture(context.Background(), account.ID, ""); err != nil {
		t.Fatal(err)
	}
	// Pi renews or replaces its provider entry in auth.json, not in Dyna's
	// older login snapshot.
	if err := os.WriteFile(auth, []byte(`{"deepseek":{"type":"api_key","key":"fixture-new"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	saved, err := accounts.SavedManager(account.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	captured, err := saved.Capture(context.Background(), "")
	if err != nil || len(captured) != 1 {
		t.Fatalf("saved capture = %+v, %v", captured, err)
	}
	raw, _ := json.Marshal(captured[0].Credential)
	if !strings.Contains(string(raw), "fixture-new") || strings.Contains(string(raw), "fixture-old") {
		t.Fatal("saved Pi account did not read its own auth.json")
	}
}

type piUsageTransport func(*http.Request) (*http.Response, error)

func (f piUsageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPiNativeManagerProbesTheProvider(t *testing.T) {
	manager, ok := NativeRuntimeManager("pi", native.Host{})
	if !ok {
		t.Fatal("Pi has no native account manager")
	}
	prober, ok := manager.(native.SavedProber)
	if !ok {
		t.Fatal("Pi manager has no saved usage probe")
	}
	old := http.DefaultTransport
	http.DefaultTransport = piUsageTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.deepseek.com/user/balance" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Fatal("wrong provider request")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"12.34"}]}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	o, err := prober.ProbeSaved(context.Background(), piUsageCredential(t, "deepseek", "fixture-key"))
	if err != nil || len(o.Windows) != 1 || o.Windows[0].Balance == nil || *o.Windows[0].Balance != 12.34 || !o.ObservedAt.After(time.Time{}) {
		t.Fatalf("Pi manager usage = %+v, %v", o, err)
	}
}

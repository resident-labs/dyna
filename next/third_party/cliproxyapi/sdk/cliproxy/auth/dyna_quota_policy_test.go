package auth

import (
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestDynaAccountCoolingOverrideBeatsConfig(t *testing.T) {
	for _, override := range []bool{true, false} {
		cfg := &config.Config{DisableCooling: !override}
		m := NewManager(nil, nil, nil)
		m.SetConfig(cfg)
		account := &Auth{Provider: "claude", Metadata: map[string]any{"disable_cooling": override}}
		if got := m.cooldownDisabledForAuth(account); got != override {
			t.Errorf("disable_cooling = %v with config %v: got %v", override, !override, got)
		}
	}
}

const dynaOpusModel = "claude-opus-5-5"

func newDynaQuotaManager(t *testing.T, ids ...string) (*Manager, *authFallbackExecutor) {
	t.Helper()
	selector := NewSessionAffinitySelector(&SoonestResetSelector{})
	t.Cleanup(selector.Stop)
	m := NewManager(nil, selector, nil)
	exec := &authFallbackExecutor{id: "claude", executeErrors: make(map[string]error)}
	m.RegisterExecutor(exec)
	for _, id := range ids {
		if _, err := m.Register(t.Context(), &Auth{ID: id, Provider: "claude", Metadata: map[string]any{DynaRequestQuotaOnly: true}}); err != nil {
			t.Fatal(err)
		}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(id, "claude", []*registry.ModelInfo{{ID: dynaOpusModel}})
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}
	return m, exec
}

func TestDynaOpusEarlyResetNeedsNoUsageRead(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		m, exec := newDynaQuotaManager(t, "early-reset")
		delay := 72 * time.Hour
		exec.executeErrors["early-reset"] = dynaTestQuotaError{hint: &delay, credential: scoped}
		req := cliproxyexecutor.Request{Model: dynaOpusModel}
		_, err := m.Execute(t.Context(), []string{"claude"}, req, resetSessionOptions("early-reset"))
		if statusCodeFromError(err) != http.StatusTooManyRequests || SafeResponseHeaders(err).Get("Retry-After") != "259200" {
			t.Fatalf("scope=%t: got error %v and headers %v", scoped, err, SafeResponseHeaders(err))
		}
		delete(exec.executeErrors, "early-reset") // The provider resets early. No reader publishes anything.
		if _, err := m.Execute(t.Context(), []string{"claude"}, req, resetSessionOptions("early-reset")); err != nil {
			t.Fatalf("scope=%t: Opus stayed down after early reset: %v", scoped, err)
		}
	}
}

type dynaTestQuotaError struct {
	hint       *time.Duration
	credential bool
}

func (e dynaTestQuotaError) Error() string              { return "weekly quota exhausted" }
func (e dynaTestQuotaError) StatusCode() int            { return http.StatusTooManyRequests }
func (e dynaTestQuotaError) RetryAfter() *time.Duration { return e.hint }
func (e dynaTestQuotaError) IsCredentialScoped() bool   { return e.credential }

func TestDynaFreshSpentAccountsNeverReachUpstream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, exec := newDynaQuotaManager(t, "spent-a", "spent-b", "spent-c", "healthy")
		req := cliproxyexecutor.Request{Model: dynaOpusModel}
		opts := resetSessionOptions("sticky-spent-account")
		resetPublish(m, "spent-a", resetFutureWeek(90, time.Hour))
		if _, err := m.Execute(t.Context(), []string{"claude"}, req, opts); err != nil {
			t.Fatal(err)
		}
		for i, id := range []string{"spent-a", "spent-b", "spent-c"} {
			week := resetFutureWeek(0, time.Duration(i+3)*24*time.Hour)
			resetPublish(m, id, week, QuotaResetWindow{UsedPercent: 100, Seconds: 18000, ResetAt: time.Now().Add(time.Hour)})
			exec.executeErrors[id] = dynaTestQuotaError{}
		}
		resetPublish(m, "healthy", resetFutureWeek(50, time.Hour))
		if _, err := m.Execute(t.Context(), []string{"claude"}, req, opts); err != nil {
			t.Fatal(err)
		}
		if got := exec.ExecuteCalls(); !slices.Equal(got, []string{"spent-a", "healthy"}) {
			t.Fatalf("fresh spent accounts were tried: %v", got)
		}
		resetPublish(m, "healthy", resetFutureWeek(0, 7*24*time.Hour))
		_, err := m.Execute(t.Context(), []string{"claude"}, req, opts)
		if statusCodeFromError(err) != http.StatusTooManyRequests || SafeResponseHeaders(err).Get("Retry-After") != "259200" || !strings.Contains(err.Error(), `"reset_seconds":259200`) {
			t.Fatalf("want the earliest credential recovery, got %v, %v", err, SafeResponseHeaders(err))
		}
		if len(exec.ExecuteCalls()) != 2 {
			t.Fatal("all-spent request reached upstream")
		}
	})
}

func TestDynaStaleReadingAllowsOneRefusalAndRequestsRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, exec := newDynaQuotaManager(t, "stale", "backup")
		req := cliproxyexecutor.Request{Model: dynaOpusModel}
		opts := resetSessionOptions("stale-reading")
		resetPublish(m, "stale", resetFutureWeek(90, time.Hour))
		resetPublish(m, "backup", resetFutureWeek(50, 48*time.Hour))
		if _, err := m.Execute(t.Context(), []string{"claude"}, req, opts); err != nil {
			t.Fatal(err)
		}
		resetPublish(m, "stale", resetFutureWeek(0, time.Hour))
		time.Sleep(SoonestResetRefreshInterval) // Advance synctest's clock, not wall time.
		resetPublish(m, "backup", resetFutureWeek(50, 48*time.Hour))
		refreshes := make(chan string, 4)
		m.SetQuotaRefresher(func(provider string, ids []string) bool {
			refreshes <- provider + ":" + strings.Join(ids, ",")
			return false
		}, nil)
		exec.executeErrors["stale"] = dynaTestQuotaError{}
		if _, err := m.Execute(t.Context(), []string{"claude"}, req, opts); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := exec.ExecuteCalls(); !slices.Equal(got, []string{"stale", "stale", "backup"}) {
			t.Fatalf("stale observation suppressed a request or caused duplicate calls: %v", got)
		}
		if len(refreshes) != 1 || <-refreshes != "claude:stale" {
			t.Fatal("quota refusal did not ask for one targeted usage refresh")
		}
	})
}

func TestDynaRefusalsQueueOneReadPerCredentialBehindProviderBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _ := newDynaQuotaManager(t, "queued-a", "queued-b")
		reads := make(chan []string, 4)
		m.SetQuotaRefresher(func(_ string, ids []string) bool {
			reads <- ids
			return len(ids) == 0 // The first provider sweep hits its usage-endpoint rate limit.
		}, nil)
		m.quotaBoard.requestRefresh(m.List(), time.Now())
		synctest.Wait()
		if len(reads) != 1 || len(<-reads) != 0 {
			t.Fatal("expected one initial provider sweep")
		}
		resetPublish(m, "queued-a", resetFutureWeek(50, 3*time.Hour))
		for _, id := range []string{"queued-a", "queued-b", "queued-a"} {
			m.MarkResult(t.Context(), Result{AuthID: id, Provider: "claude", Model: dynaOpusModel, Error: &Error{HTTPStatus: 429, Message: "weekly quota exhausted"}})
		}
		account, _ := m.GetByID("queued-a")
		if _, ok := m.quotaBoard.hint(account); ok {
			t.Fatal("refusal left the prior usage observation eligible for ranking")
		}
		time.Sleep(2*SoonestResetRefreshInterval - time.Second)
		synctest.Wait()
		if len(reads) != 0 {
			t.Fatal("refusal bypassed the usage-endpoint backoff")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if len(reads) != 1 {
			t.Fatalf("queued reads after backoff: %d; want 1", len(reads))
		}
		ids := <-reads
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"queued-a", "queued-b"}) {
			t.Fatalf("refusing credentials to refresh: %v", ids)
		}
	})
}

func TestDynaQuotaOnlyStreamBootstrapMarksTerminalFailure(t *testing.T) {
	for _, status := range []int{429, 503} {
		m, exec := newDynaQuotaManager(t, "bootstrap-a", "bootstrap-b")
		exec.streamFirstErrors = map[string]error{
			"bootstrap-a": &Error{HTTPStatus: status, Message: "first account failed"},
			"bootstrap-b": dynaTestQuotaError{},
		}
		ids := []string{base64.RawURLEncoding.EncodeToString([]byte("bootstrap-a")), base64.RawURLEncoding.EncodeToString([]byte("bootstrap-b"))}
		ctx := WithDynaEligibleAuth(t.Context(), ids)
		resetPublish(m, "bootstrap-a", resetFutureWeek(50, time.Hour))
		resetPublish(m, "bootstrap-b", resetFutureWeek(50, 2*time.Hour))
		stream, err := m.ExecuteStream(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: dynaOpusModel}, cliproxyexecutor.Options{})
		if err == nil {
			for chunk := range stream.Chunks {
				err = chunk.Err
			}
		}
		if err == nil || IsDynaTerminalQuota(err) != (status == 429) || !slices.Equal(exec.StreamCalls(), []string{"bootstrap-a", "bootstrap-b"}) {
			t.Fatalf("first status=%d: err=%v, calls=%v", status, err, exec.StreamCalls())
		}
	}
}

func TestDynaClaudeForbiddenStillQuarantines(t *testing.T) {
	m, exec := newDynaQuotaManager(t, "forbidden")
	exec.executeErrors["forbidden"] = &Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"}
	req := cliproxyexecutor.Request{Model: dynaOpusModel}
	if _, err := m.Execute(t.Context(), []string{"claude"}, req, cliproxyexecutor.Options{}); statusCodeFromError(err) != http.StatusForbidden {
		t.Fatalf("first request: %v", err)
	}
	if _, err := m.Execute(t.Context(), []string{"claude"}, req, cliproxyexecutor.Options{}); err == nil || len(exec.ExecuteCalls()) != 1 {
		t.Fatalf("403 quarantine was weakened: error=%v, calls=%v", err, exec.ExecuteCalls())
	}
}

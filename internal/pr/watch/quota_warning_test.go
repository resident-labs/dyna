package watch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

func TestQuotaWarningUsesScopedObservedLimitAndDedupes(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "quota-test"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(time.Hour)
	rates := map[string]RateLimit{}
	gh := GitHub{run: func(_ context.Context, cmd *exec.Cmd) error {
		fmt.Fprintf(cmd.Stdout, "HTTP/2.0 200 OK\r\nX-RateLimit-Resource: core\r\nX-RateLimit-Limit: 5000\r\nX-RateLimit-Remaining: 1000\r\nX-RateLimit-Reset: %d\r\n\r\n{}", reset.Unix())
		return nil
	}}
	gh.requests = &requestCache{rates: rates, used: map[string]cachedResponse{}, now: func() time.Time { return now }}
	if _, _, err := gh.request(t.Context(), Ref{Host: "github.com", Repo: "owner/repo"}, "repos/owner/repo"); err != nil {
		t.Fatal(err)
	}
	// The direct fixture has no selected credential; transfer its observation to
	// the same scoped key the real account selector supplies.
	rate := rates[(RateLimitScope{Host: "github.com", Resource: "core"}).Key()]
	scope := RateLimitScope{Host: "github.com", Account: "private-login", Resource: "core"}
	if rate.Limit == nil || *rate.Limit != 5000 {
		t.Fatalf("missing limit: %+v", rate)
	}
	st := State{Records: map[string]*Record{"a": {RoomID: rec.ID, Host: scope.Host, QuotaAccount: scope.Account, Watching: true}}, RateLimits: map[string]RateLimit{scope.Key(): rate}}
	s := &Service{}
	s.postQuotaWarnings(st, now)
	s.postQuotaWarnings(st, now)
	messages, err := rooms.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || strings.Contains(fmt.Sprintf("%+v", messages[0]), "private-login") {
		t.Fatalf("messages = %+v", messages)
	}
	rate.Reset = reset.Add(time.Hour)
	st.RateLimits[scope.Key()] = rate
	s.postQuotaWarnings(st, now)
	messages, err = rooms.Messages(rec.ID)
	if err != nil || len(messages) != 2 {
		t.Fatalf("new reset: %+v, %v", messages, err)
	}
}

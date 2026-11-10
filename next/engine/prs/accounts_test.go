package prs

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestAccountClientsSeparateQuotasAndRotate(t *testing.T) {
	now := time.Now()
	token := "work-token"
	accounts := &Accounts{Transport: http.DefaultTransport, Now: func() time.Time { return now }, Resolve: func(_ context.Context, repo contract.RepoName) (string, string, error) {
		if repo == "org/work" {
			return token, "work", nil
		}
		return "engine-token", "", nil
	}}
	work, err := accounts.Repo(t.Context(), "org/work")
	if err != nil {
		t.Fatal(err)
	}
	personal, err := accounts.Repo(t.Context(), "org/personal")
	if err != nil {
		t.Fatal(err)
	}
	work.rest.note(5000, 1, now.Add(time.Hour))
	if _, low := personal.Low(); low {
		t.Fatal("account quotas shared")
	}
	poller := &Poller{GitHub: personal, Accounts: accounts, Now: func() time.Time { return now }}
	repos := map[contract.RepoName]bool{"org/work": true, "org/personal": true}
	if len(poller.due(t.Context(), repos)) != 2 {
		t.Fatal("first pass not due")
	}
	now = now.Add(time.Minute)
	due := poller.due(t.Context(), repos)
	if len(due) != 1 || due[0] != "org/personal" {
		t.Fatalf("per-account low quota due: %v", due)
	}
	token = "replacement"
	rotated, err := accounts.Repo(t.Context(), "org/work")
	if err != nil {
		t.Fatal(err)
	}
	if rotated == work || rotated.Token != token {
		t.Fatal("rotation reused old credential client")
	}
}

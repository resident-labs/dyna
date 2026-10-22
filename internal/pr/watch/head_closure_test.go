package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store/storetest"
)

func TestHeadRefreshStoresClosureTime(t *testing.T) {
	storetest.DataDir(t)
	observed := time.Date(2099, 1, 3, 12, 0, 0, 0, time.UTC)
	now := observed
	s := &Service{Store: &Store{Root: filepath.Join(t.TempDir(), "prs")}, Now: func() time.Time { return now }}
	s.GH = GitHub{now: s.Now, run: func(_ context.Context, cmd *exec.Cmd) error {
		_, err := fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n"+`{"data":{"r0":{"defaultBranchRef":{"name":"main"},"p1":{"number":1,"state":"CLOSED","merged":false,"headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","baseRefName":"main"},"p2":{"number":2,"state":"MERGED","merged":true,"headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","baseRefName":"main"}}}}`)
		return err
	}}
	if err := s.Store.update(func(st *State) error {
		for i := 1; i <= 2; i++ {
			key := fmt.Sprint(i)
			st.Records[key] = &Record{Key: key, Host: "github.com", Repo: "owner/repo", Number: i, Watching: true, State: "open", Head: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Created: observed.Add(-48 * time.Hour), Checked: observed.Add(-time.Hour), Polling: Polling{Next: observed.Add(time.Hour)}}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(s.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		st, err := s.Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.refreshHeads(t.Context(), st, now, nil); err != nil {
			t.Fatal(err)
		}
		for i, want := range []string{"closed", "merged"} {
			var body []byte
			if err := db.QueryRow("SELECT body FROM pr_records WHERE key=?", fmt.Sprint(i+1)).Scan(&body); err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Record Record `json:"record"`
			}
			if err := json.Unmarshal(body, &saved); err != nil {
				t.Fatal(err)
			}
			got := saved.Record
			if got.State != want || !got.ClosedAt.Equal(observed) || !got.Polling.Next.IsZero() {
				t.Fatalf("pass %d: stored terminal record = %+v; want %s at %s", pass, got, want, observed)
			}
		}
		now = now.Add(time.Hour)
	}
}

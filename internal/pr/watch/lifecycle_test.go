package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
)

func TestOwnershipPublicationRejectsClosingAndClosed(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "closing", true: "closed"}[closed], func(t *testing.T) {
			e := newEnv(t)
			meta := e.author.Meta
			if closed {
				meta.Closed = time.Now()
			} else {
				meta.Closing = &sessionlife.Closing{Token: "checking", PID: os.Getpid()}
			}
			recordtest.Write(t, filepath.Join(filepath.Dir(e.s.Store.Root), "sessions"), e.author.ID, meta)
			if _, _, err := e.s.Track(context.Background(), authority.Actor{Role: authority.Owner}, Ref{Host: "github.com", Repo: "owner/repo", Number: 17}, e.author, e.author.Dir); err == nil {
				t.Fatal("tracked stale author snapshot")
			}
			state, _ := e.s.Store.Read()
			if len(state.Records) != 0 {
				t.Fatal("published rejected ownership")
			}
		})
	}
}

func TestRolePublicationClearsOwnerDormancy(t *testing.T) {
	for _, action := range []string{"track PR", "open change", "transfer author", "track closed PR"} {
		t.Run(action, func(t *testing.T) {
			e := newEnv(t)
			target := e.author
			var r Record
			if action == "transfer author" {
				r = e.track()
				target = transferTarget(e)
			}
			meta := target.Meta
			meta.Status = "dormant"
			meta.Finished = time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
			sessions := filepath.Join(filepath.Dir(e.s.Store.Root), "sessions")
			recordtest.Write(t, sessions, target.ID, meta)
			switch action {
			case "track closed PR":
				e.f.Pull.State = "closed"
				e.write()
				e.track()
			case "track PR":
				e.track()
			case "open change":
				localChange(t, e)
			case "transfer author":
				if err := e.s.Transfer(t.Context(), r.Key, "author", target, ""); err != nil {
					t.Fatal(err)
				}
			}
			data, err := recordtest.ReadRaw(t, sessions, target.ID)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Status   string
				Finished time.Time
				ID       string
			}
			if err = json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if action == "track closed PR" {
				if got.Status != "dormant" || !got.Finished.Equal(meta.Finished) {
					t.Fatalf("closed PR changed dormant owner: %s", data)
				}
				return
			}
			if got.Status != "idle" || !got.Finished.IsZero() || got.ID != target.ID {
				t.Fatalf("role owner metadata = %s", data)
			}
		})
	}
}

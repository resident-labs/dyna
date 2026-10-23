package view_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	prview "dyna-agent/internal/pr/view"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
)

func publicationEnv(t *testing.T) (*env, room.Record, watch.Ref, string) {
	t.Helper()
	e := newEnv(t)
	rec := e.room("publication")
	roomfixture.Repository(t, e.svc.Notify.Rooms, rec.ID, "github.com", "owner/repo", "read-write")
	ref := watch.Ref{Host: "github.com", Repo: "owner/repo", Number: 17}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, watch.State{Version: 1, Records: map[string]*watch.Record{
		ref.Key(): {Key: ref.Key(), Host: ref.Host, Repo: ref.Repo, Number: ref.Number, RoomID: rec.ID, Head: "head"},
	}})
	writes := filepath.Join(t.TempDir(), "writes")
	t.Setenv("PUBLICATION_WRITES", writes)
	fake := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$*" in *--include*) printf 'HTTP/2.0 200 OK\r\n\r\n';; esac
case "$*" in
 *"--method POST"*) printf '%s\n' "$*" >> "$PUBLICATION_WRITES"; printf '{"id":123}';;
 *"issues/17/comments?"*) printf '[{"id":12,"body":"Automated notice: daily quota reached. Try again tomorrow.","user":{"login":"quota[bot]","type":"Bot"}}]';;
 *"/reviews?"*|*"/comments?"*) printf '[]';;
 *) printf '{"user":{"login":"owner"},"head":{"sha":"head"}}';;
esac
`
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ghclient.BinaryEnv, fake)
	return e, rec, ref, writes
}

func TestPublicationUsesAuthenticatedSession(t *testing.T) {
	for _, role := range []authority.Role{authority.Owner, authority.Root, authority.Session, authority.Worker, ""} {
		t.Run(string(role), func(t *testing.T) {
			e, rec, ref, writes := publicationEnv(t)
			// Agents cannot publish, regardless of an authenticated session or explicit flag.
			actor := authority.Actor{Role: role, Room: rec.ID, Session: "ses_authenticated", Run: "run_worker"}
			ctx := authority.WithActor(t.Context(), actor)
			body := "<!-- dyna:ses_forged -->" + " feedback"
			if role != authority.Owner {
				for _, publish := range []bool{false, true} {
					if err := e.svc.PRs.CommentPR(ctx, rec.ID, ref, body, publish); err == nil || !strings.Contains(err.Error(), "agents cannot publish") {
						t.Fatalf("agent comment = %v", err)
					}
					if _, err := e.svc.PRs.PostPRReview(ctx, rec.ID, ref, body, "head", nil, publish); err == nil || !strings.Contains(err.Error(), "agents cannot publish") {
						t.Fatalf("agent review = %v", err)
					}
				}
				if err := e.svc.PRs.SetReviewPublishing(ctx, rec.ID, watch.PublishingGitHub); err == nil {
					t.Fatal("agent enabled GitHub publishing")
				}
				if _, err := e.svc.PRs.SetPRSettings(ctx, rec.ID, prview.PRConfig{ReviewPublishing: watch.PublishingGitHub}); err == nil {
					t.Fatal("agent enabled GitHub publishing through settings")
				}
				if _, err := os.Stat(writes); !os.IsNotExist(err) {
					t.Fatalf("agent wrote to GitHub: %v", err)
				}
				return
			}
			if err := e.svc.PRs.CommentPR(ctx, rec.ID, ref, body, true); err != nil {
				t.Fatal(err)
			}
			id, err := e.svc.PRs.PostPRReview(ctx, rec.ID, ref, body, "", []watch.ReviewComment{{Path: "file.go", Line: 1, Body: "feedback"}}, true)
			if err != nil || id != 123 {
				t.Fatalf("review = %d, %v", id, err)
			}
			result, err := e.svc.PRs.LearnPRNoise(ctx, rec.ID, ref.Key(), "issue-comments:12", "spam")
			if err != nil || !result.Created {
				t.Fatalf("noise = %+v, %v", result, err)
			}
			want := actor.Session
			if role == authority.Owner || role == authority.Worker {
				want = ""
			}
			st, err := e.svc.PRs.Watcher().Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"issue-comments:123", "reviews:123"} {
				if got := st.CommentSessions[key]; got != want {
					t.Errorf("%s belongs to %q, want %q", key, got, want)
				}
			}
			if result.Rule.LearnedBy != want {
				t.Errorf("learned by %q, want %q", result.Rule.LearnedBy, want)
			}
		})
	}
}

func TestPublicationRejectsOtherRoomsBeforeGitHub(t *testing.T) {
	e, rec, ref, writes := publicationEnv(t)
	other := e.room("other")
	for _, tc := range []struct {
		name, room string
		actor      authority.Actor
	}{
		{"session to foreign room", rec.ID, authority.Actor{Role: authority.Session, Room: other.ID}},
		{"root to foreign room", rec.ID, authority.Actor{Role: authority.Root, Room: other.ID}},
		{"worker to foreign room", rec.ID, authority.Actor{Role: authority.Worker, Room: other.ID}},
		{"owner with wrong PR room", other.ID, authority.Actor{Role: authority.Owner}},
		{"session with wrong PR room", other.ID, authority.Actor{Role: authority.Session, Room: other.ID}},
		{"no caller", rec.ID, authority.Actor{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := authority.WithActor(t.Context(), tc.actor)
			if err := e.svc.PRs.CommentPR(ctx, tc.room, ref, "feedback", true); err == nil {
				t.Fatal("comment accepted")
			}
			if _, err := e.svc.PRs.PostPRReview(ctx, tc.room, ref, "feedback", "head", nil, true); err == nil {
				t.Fatal("review accepted")
			}
			if _, err := e.svc.PRs.LearnPRNoise(ctx, tc.room, ref.Key(), "issue-comments:12", "spam"); err == nil {
				t.Fatal("noise accepted")
			}
		})
	}
	if _, err := os.Stat(writes); !os.IsNotExist(err) {
		t.Fatalf("GitHub writes: %v", err)
	}
}

func TestPublicationPreservesPolicyAndUntrackedOptIn(t *testing.T) {
	e, rec, ref, _ := publicationEnv(t)
	// A second room for the same repository makes an absent room context ambiguous.
	roomfixture.Repository(t, e.svc.Notify.Rooms, e.room("same-repository").ID, "github.com", "owner/repo", "read-only")
	ctx := authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner, Room: rec.ID})
	for _, tracked := range []bool{true, false} {
		if !tracked {
			ref.Number++
		}
		for _, publish := range []bool{false, true} {
			err := e.svc.PRs.CommentPR(ctx, rec.ID, ref, "feedback", publish)
			if publish && err != nil {
				t.Fatalf("explicit comment: %v", err)
			}
			if !publish && (err == nil || !strings.Contains(err.Error(), "--publish")) {
				t.Fatalf("comment policy: %v", err)
			}
			_, err = e.svc.PRs.PostPRReview(ctx, rec.ID, ref, "feedback", "head", nil, publish)
			if publish && err != nil {
				t.Fatalf("explicit review: %v", err)
			}
			if !publish && (err == nil || !strings.Contains(err.Error(), "--publish")) {
				t.Fatalf("review policy: %v", err)
			}
		}
	}
	ref.Number = 17
	if err := e.svc.PRs.SetReviewPublishing(ownerCtx, rec.ID, watch.PublishingGitHub); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PRs.CommentPR(ctx, rec.ID, ref, "feedback", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PRs.PostPRReview(ctx, rec.ID, ref, "feedback", "", nil, false); err != nil {
		t.Fatal(err)
	}
}

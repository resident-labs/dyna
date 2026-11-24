package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	gitroute "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/store"
)

func TestPushToAnotherRoomRepository(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "r", "r", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []contract.RepoName{"o/primary", "o/secondary"} {
		if err := s.AddRoomRepo(ctx, "r", repo, repo == "o/primary"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,room,name,repo,profile,state,goal,created_at) VALUES
			('a','r','work','o/primary','p','open','',0),('b','r','other','o/Secondary','p','open','',0)`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at)
			SELECT 'box-'||id,id,'','agent','ready','','',0,0 FROM sessions`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"main", "master", "HEAD", "work"} {
		if err := s.AddBranch(ctx, "o/primary", "a", branch, time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddBranch(ctx, "o/Secondary", "b", "work-taken", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBranch(ctx, "o/primary", "b", "work-source-held", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		repo     contract.RepoName
		branches []string
		status   int
		forward  bool
		claims   []string
	}{
		{"own branch and topic", "o/secondary", []string{"work", "work-fix"}, http.StatusOK, true, []string{"work-fix", "work"}},
		{"already reserved", "o/secondary", []string{"work"}, http.StatusOK, true, nil},
		{"held in destination", "o/secondary", []string{"work-taken"}, http.StatusOK, false, nil},
		{"held in source", "o/secondary", []string{"work-source-held"}, http.StatusOK, false, nil},
		{"all or nothing", "o/secondary", []string{"work-mixed", "work-taken"}, http.StatusOK, false, nil},
		{"another session claims first", "o/secondary", []string{"work-mixed", "work-raced"}, http.StatusOK, false, nil},
		{"not in room", "o/outside", []string{"work"}, http.StatusForbidden, false, nil},
		{"main", "o/secondary", []string{"main"}, http.StatusOK, false, nil},
		{"master", "o/secondary", []string{"master"}, http.StatusOK, false, nil},
		{"HEAD", "o/secondary", []string{"HEAD"}, http.StatusOK, false, nil},
		{"default branch", "o/secondary", []string{"work-default"}, http.StatusOK, false, nil},
		{"primary default matching a topic", "o/primary", []string{"work-default"}, http.StatusOK, false, nil},
		{"ordinary own-repo push", "o/primary", []string{"work"}, http.StatusOK, true, nil},
		{"primary cache miss", "o/primary", []string{"work-cache-miss"}, http.StatusOK, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := s.SessionBranches(ctx, "o/secondary", "a")
			if err != nil {
				t.Fatal(err)
			}
			var body strings.Builder
			for i, branch := range tc.branches {
				line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/" + branch
				if i == 0 {
					line += "\x00report-status"
				}
				fmt.Fprintf(&body, "%04x%s", len(line)+4, line)
			}
			body.WriteString("0000PACK")
			target := &url.URL{Scheme: "https", Host: "github.com"}
			var forwarded bool
			transport := pushTransport(func(r *http.Request) (*http.Response, error) {
				forwarded = true
				if r.URL.Path != "/"+string(tc.repo)+".git/git-receive-pack" {
					t.Errorf("wrong upstream path %s", r.URL.Path)
				}
				for _, branch := range tc.claims {
					if holder, err := s.BranchHolder(ctx, tc.repo, branch); err != nil || holder != "a" {
						t.Errorf("branch %q not reserved before forwarding: %q, %v", branch, holder, err)
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})
			w := httptest.NewRecorder()
			gitroute.Handler(target, transport, "secret", "a", "r", s, func(_ context.Context, repo contract.RepoName, remote bool) (string, error) {
				if repo != tc.repo || remote != (tc.repo != "o/primary") {
					t.Errorf("unexpected default-branch lookup for %q with remote=%t while pushing to %q", repo, remote, tc.repo)
				}
				if slices.Contains(tc.branches, "work-cache-miss") {
					return "", nil
				}
				if slices.Contains(tc.branches, "work-raced") {
					if err := s.AddBranch(ctx, repo, "b", "work-raced", time.Unix(2, 0)); err != nil {
						t.Fatal(err)
					}
				}
				return "work-default", nil
			}).ServeHTTP(w, httptest.NewRequest("POST", "/git/"+string(tc.repo)+".git/git-receive-pack", strings.NewReader(body.String())))
			if w.Code != tc.status || forwarded != tc.forward {
				t.Errorf("response %d %q, forwarded %v", w.Code, w.Body.String(), forwarded)
			}
			if !tc.forward && tc.status == http.StatusOK && !strings.Contains(w.Body.String(), "work-<topic>") {
				t.Errorf("missing branch guidance: %q", w.Body.String())
			}
			after, err := s.SessionBranches(ctx, "o/secondary", "a")
			want := append(slices.Clone(tc.claims), before...)
			if err != nil || !slices.Equal(after, want) {
				t.Errorf("recorded %v (%v), want %v", after, err, want)
			}
		})
	}
	for repo, want := range map[contract.RepoName]bool{"o/primary": true, "o/secondary": false} {
		if got, err := gitroute.CanRunActionsOnBranch(ctx, s, repo, "a", "work"); err != nil || got != want {
			t.Errorf("Actions on %s: %v, %v; want %v", repo, got, err, want)
		}
	}
}

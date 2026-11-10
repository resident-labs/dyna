package git

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/githubaccount"
	"github.com/resident-labs/dyna/engine/store"
)

func TestRoomCredentialsOnGitRouteAndMirror(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	files := &githubaccount.Files{Dir: t.TempDir()}
	for _, room := range []contract.RoomID{"default", "work"} {
		if err := st.CreateRoom(ctx, room, string(room), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := files.Write("key", "work-secret"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGitHubAccount(ctx, "work", store.GitHubIdentity{Login: "work", Credential: "key"}); err != nil {
		t.Fatal(err)
	}
	resolver := &githubaccount.Resolver{Store: st, Tokens: files, DefaultToken: "engine-secret"}
	for _, tc := range []struct {
		room  contract.RoomID
		token string
	}{{"default", "engine-secret"}, {"work", "work-secret"}} {
		repo := contract.RepoName("owner/" + string(tc.room))
		if err := st.AddRoomRepo(ctx, tc.room, repo, true); err != nil {
			t.Fatal(err)
		}
		mirror := &Mirror{Token: "engine-secret", ResolveToken: resolver.Repo}
		env, err := mirror.authEnv(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(env, "\n"), base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tc.token))) {
			t.Fatal("mirror used wrong account")
		}
		token, _, err := resolver.Room(ctx, tc.room)
		if err != nil {
			t.Fatal(err)
		}
		target, _ := url.Parse("https://github.com")
		seen := 0
		transport := roundTrip(func(r *http.Request) (*http.Response, error) {
			seen++
			_, password, ok := r.BasicAuth()
			if !ok || password != tc.token {
				t.Error("route used wrong account")
			}
			return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("Bad credentials"))}, nil
		})
		handler := Handler(target, transport, token, "ses_1", tc.room, routeAccess())
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "/git/owner/repo.git/info/refs?service=git-upload-pack", nil))
		if response.Code != 401 || seen != 1 {
			t.Fatalf("response %d calls %d", response.Code, seen)
		}
	}
	if err := files.Clear("key"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolver.Repo(ctx, "owner/work"); err == nil {
		t.Fatal("missing token fell back")
	}
}

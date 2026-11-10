package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/githubaccount"
	"github.com/resident-labs/dyna/engine/githubwrites"
	"github.com/resident-labs/dyna/engine/prs"
	"github.com/resident-labs/dyna/engine/store"
)

type toolAccounts struct{ accountToolStore }

func (toolAccounts) Session(_ context.Context, id contract.SessionID) (store.Session, error) {
	return store.Session{Room: contract.RoomID(id)}, nil
}
func (toolAccounts) GitHubAccount(_ context.Context, room contract.RoomID) (githubaccount.Account, error) {
	if room == "work" {
		return githubaccount.Account{Login: "work", Credential: "key"}, nil
	}
	return githubaccount.Account{}, nil
}
func (toolAccounts) RepoRoom(context.Context, contract.RepoName) (contract.RoomID, error) {
	return "", nil
}

func TestAccountToolsSharesDefaultHandlerAcrossRooms(t *testing.T) {
	accounts := toolAccounts{}
	files := &githubaccount.Files{Dir: t.TempDir()}
	if err := files.Write("key", "work-token"); err != nil {
		t.Fatal(err)
	}
	resolver := &githubaccount.Resolver{Store: accounts, Tokens: files, DefaultToken: "engine-token"}
	clients := &prs.Accounts{Transport: http.DefaultTransport, Now: time.Now}
	built := map[*prs.Client]int{}
	handler := accountTools(t.Context(), accounts, resolver, clients, func(client *prs.Client) (http.Handler, error) {
		built[client]++
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Account", client.Token) }), nil
	})
	for _, room := range []string{"ordinary-a", "ordinary-b", "work", "ordinary-a", "work"} {
		req := httptest.NewRequest(http.MethodPost, "/github", nil)
		req = req.WithContext(githubwrites.WithSession(req.Context(), contract.SessionID(room)))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		expected := "engine-token"
		if room == "work" {
			expected = "work-token"
		}
		if response.Header().Get("Account") != expected {
			t.Fatal("incorrect account handler")
		}
	}
	if len(built) != 2 {
		t.Fatalf("constructed %d handlers, want shared default and separate work handler", len(built))
	}
	for _, count := range built {
		if count != 1 {
			t.Fatal("built handler more than once")
		}
	}
}

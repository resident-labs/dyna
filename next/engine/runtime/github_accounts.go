package runtime

import (
	"context"
	"net/http"
	"sync"

	"github.com/resident-labs/dyna/contract"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/githubaccount"
	"github.com/resident-labs/dyna/engine/githubmcp"
	"github.com/resident-labs/dyna/engine/githubwrites"
	"github.com/resident-labs/dyna/engine/prs"
	"github.com/resident-labs/dyna/engine/store"
)

func accountTools(ctx context.Context, st *store.Store, credentials *githubaccount.Resolver, clients *prs.Accounts) http.Handler {
	var mu sync.Mutex
	type cached struct {
		client  *prs.Client
		handler http.Handler
	}
	handlers := map[contract.RoomID]cached{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, err := st.Session(r.Context(), githubwrites.SessionFrom(r.Context()))
		if err != nil {
			http.Error(w, "GitHub caller unavailable", http.StatusServiceUnavailable)
			return
		}
		token, login, err := credentials.Room(r.Context(), session.Room)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if login != "" {
			login += " of room " + string(session.Room)
		}
		client := clients.Client(token, login)
		mu.Lock()
		held := handlers[session.Room]
		if held.client != client {
			handler, buildErr := githubmcp.Handler(ctx, token, client.Reads(client.Transport), func(ctx context.Context, repo contract.RepoName, number int) (bool, error) {
				return st.OwnsPR(ctx, repo, githubwrites.SessionFrom(ctx), number)
			}, func(ctx context.Context, repo contract.RepoName, branch string) (bool, error) {
				return enginegit.CanPushBranch(ctx, st, repo, githubwrites.SessionFrom(ctx), branch)
			})
			if buildErr != nil {
				mu.Unlock()
				http.Error(w, "GitHub tools unavailable", http.StatusServiceUnavailable)
				return
			}
			held = cached{client, handler}
			handlers[session.Room] = held
		}
		mu.Unlock()
		held.handler.ServeHTTP(w, r)
	})
}

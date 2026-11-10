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
)

type accountToolStore interface {
	enginegit.RoomRepositories
	OwnsPR(context.Context, contract.RepoName, contract.SessionID, int) (bool, error)
}

func accountTools(ctx context.Context, st accountToolStore, credentials *githubaccount.Resolver, clients *prs.Accounts, builders ...func(*prs.Client) (http.Handler, error)) http.Handler {
	type cached struct {
		once    sync.Once
		handler http.Handler
		err     error
	}
	var handlers sync.Map // account client to its one MCP handler and read semaphore
	build := func(client *prs.Client) (http.Handler, error) {
		return githubmcp.Handler(ctx, client.Token, client.Reads(client.Transport), func(ctx context.Context, repo contract.RepoName, number int) (bool, error) {
			return st.OwnsPR(ctx, repo, githubwrites.SessionFrom(ctx), number)
		}, func(ctx context.Context, repo contract.RepoName, branch string) (bool, error) {
			return enginegit.CanPushBranch(ctx, st, repo, githubwrites.SessionFrom(ctx), branch)
		})
	}
	if len(builders) > 0 {
		build = builders[0]
	}
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
		value, _ := handlers.LoadOrStore(client, &cached{})
		held := value.(*cached)
		held.once.Do(func() { held.handler, held.err = build(client) })
		if held.err != nil {
			handlers.CompareAndDelete(client, held)
			http.Error(w, "GitHub tools unavailable", http.StatusServiceUnavailable)
			return
		}

		held.handler.ServeHTTP(w, r)
	})
}

package model

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/private"
)

// localRoutes adds what Claude Code and Codex ask for besides a model call.
var localRoutes = map[string]string{
	"/model/v1/messages":              http.MethodPost,
	"/model/v1/messages/count_tokens": http.MethodPost,
	"/model/v1/chat/completions":      http.MethodPost,
	"/model/v1/responses":             http.MethodPost,
	"/model/v1/responses/compact":     http.MethodPost,
	"/model/v1/models":                http.MethodGet,
}

// LocalStore gives the route the key and accounts as they are now, so a reset or a new account applies to the next request.
type LocalStore interface {
	LocalProxyKey(context.Context) (string, error)
	Accounts(context.Context) ([]private.AccountBinding, error)
}

// Local returns the route for the owner's own harnesses. It takes any model and every
// account, because the owner holds no profile or room, and the proxy refuses a model no
// account serves. It is the box route's policy with one fixed affinity and no pin.
func (p *Proxy) Local(st LocalStore) http.Handler {
	anyModel := func(string) bool { return true }
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, err := st.LocalProxyKey(r.Context())
		if err != nil {
			refuse(w, http.StatusServiceUnavailable, "unavailable", "The proxy key could not be read.")
			return
		}
		if !localKeyMatches(r, key) {
			refuse(w, http.StatusUnauthorized, "invalid_key", "The proxy key is not valid. Run dyna proxy env for the current one.")
			return
		}
		accounts, err := st.Accounts(r.Context())
		if err != nil {
			refuse(w, http.StatusServiceUnavailable, "unavailable", "Accounts could not be read.")
			return
		}
		if len(accounts) == 0 {
			refuse(w, http.StatusServiceUnavailable, "no_account", "No account is connected to Dyna.")
			return
		}
		ids := make([]string, len(accounts))
		for i, account := range accounts {
			ids[i] = account.ProxyID
		}
		serve(p.client, p.clientKey, contract.LocalClient, localRoutes, anyModel, ids, "", false).ServeHTTP(w, r)
	})
}

// localKeyMatches accepts the key as harnesses send it, in Authorization or X-Api-Key.
// An empty stored key means none was made yet, so nothing matches it.
func localKeyMatches(r *http.Request, key string) bool {
	if key == "" {
		return false
	}
	presented := r.Header.Get("X-Api-Key")
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		presented = bearer
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(key)) == 1
}

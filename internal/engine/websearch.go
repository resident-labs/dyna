package engine

import (
	"context"
	"errors"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/websearch"
	"dyna-agent/internal/websvc"
)

// webSearchBinding searches the web for one session. It reads credentials but
// changes no state, so it is a read call on the engine's private socket: only a
// local process asks, and the provider token never leaves the engine.
func (s services) webSearchBinding() websvc.Binding {
	return websvc.Binding{Name: "WebSearch", Access: websvc.Read, Scope: websvc.ScopeOperate, Local: true, Method: s.webSearch}
}

func (s services) webSearch(ctx context.Context, in callspec.WebSearchRequest) (callspec.WebSearchResponse, error) {
	room, repo := s.searchScope(ctx)
	results, err := s.webSearchChain(room, repo).Search(ctx, websearch.Query{Text: in.Query, Limit: in.Limit})
	if err != nil {
		return callspec.WebSearchResponse{}, err
	}
	return callspec.WebSearchResponse{Results: results}, nil
}

// searchScope names the room and repository a search comes from: the calling
// session's own room, and the repository of the directory that session runs in.
// Both come from the verified caller and the session's stored record, never
// from a call argument, so a session that runs in one repository cannot name
// another and spend an account the owner limited to it. A caller with no stored
// directory counts as having no repository, which the repo rules refuse for
// every limited account.
func (s services) searchScope(ctx context.Context) (room, repo string) {
	actor := authority.Of(ctx)
	if actor.Session == "" || s.rooms == nil || s.rooms.Notify.Sessions == nil {
		return actor.Room, ""
	}
	sessions := s.rooms.Notify.Sessions
	meta, err := sessions.Store.ReadMeta(actor.Session)
	if err != nil {
		return actor.Room, ""
	}
	dir, _ := sessions.EffectiveDir(meta)
	if dir == "" {
		return actor.Room, ""
	}
	return actor.Room, harnessruntime.RepoIdentity(ctx, dir)
}

// webSearchChain is the provider order for every search: the owner's ChatGPT
// sign-in first, because it answers with sources, then the Brave Search API
// when the owner saved a key for it. Every credential is read for the room and
// repository of this search.
func (s services) webSearchChain(room, repo string) websearch.Chain {
	accounts := s.runtime.Accounts
	return websearch.Chain{
		websearch.ChatGPT{Credential: piChatGPTCredential{accounts: accounts, room: room, repo: repo}},
		websearch.Brave{Key: piProviderKey{accounts: accounts, provider: store.ProviderBrave, room: room, repo: repo}.read},
	}
}

// piChatGPTCredential reads the Pi ChatGPT login that may run in the search's
// room and repository.
type piChatGPTCredential struct {
	accounts   *harnessruntime.Accounts
	room, repo string
	// Renew runs before the read when the engine has a renewal to do. PR #472
	// adds the renewal script that sets it.
	Renew func(ctx context.Context) error
}

// Token is the access token and account ID of the ChatGPT sign-in.
func (c piChatGPTCredential) Token(ctx context.Context) (access, account string, err error) {
	if c.Renew != nil {
		if err := c.Renew(ctx); err != nil {
			return "", "", err
		}
	}
	access, account, err = c.accounts.PiChatGPTLogin(ctx, c.room, c.repo)
	if errors.Is(err, native.ErrNotSignedIn) {
		return "", "", websearch.ErrNotSignedIn
	}
	if err != nil {
		return "", "", err
	}
	return access, account, nil
}

// piProviderKey reads the API key a provider's account saved for the search's
// room and repository.
type piProviderKey struct {
	accounts   *harnessruntime.Accounts
	provider   store.Provider
	room, repo string
}

func (k piProviderKey) read(ctx context.Context) (string, error) {
	key, err := k.accounts.PiProviderKey(ctx, k.provider, k.room, k.repo)
	if errors.Is(err, native.ErrNotSignedIn) {
		return "", websearch.ErrNotSignedIn
	}
	return key, err
}

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

// webSearchChain is the provider order for every search. The providers the
// owner is signed in to answer first, so a search costs no API key when one of
// them can answer; the plain API-key providers come last, in the order the
// owner named them. Every credential is read for the room and repository of
// this search.
//
// A provider whose account or key is missing returns ErrNotSignedIn, so the
// chain skips it and no search ever uses a credential the owner did not save.
func (s services) webSearchChain(room, repo string) websearch.Chain {
	accounts := s.runtime.Accounts
	return websearch.Chain{
		websearch.ChatGPT{Credential: piChatGPTCredential{accounts: accounts, room: room, repo: repo}},
		websearch.Claude{Token: savedClaudeToken{accounts: accounts, room: room, repo: repo}.read},
		websearch.Muse{Key: piInferenceKey{accounts: accounts, provider: store.ProviderMetaMuse, room: room, repo: repo}.read},
		websearch.Kimi{Token: piAccessToken{accounts: accounts, provider: store.ProviderKimiCode, room: room, repo: repo}.read},
		websearch.Zai{Key: piProviderKey{accounts: accounts, provider: store.ProviderZAI, room: room, repo: repo}.read},
		websearch.XAI{Token: piAccessToken{accounts: accounts, provider: store.ProviderXAI, room: room, repo: repo}.read},
		websearch.Brave{Key: piProviderKey{accounts: accounts, provider: store.ProviderBrave, room: room, repo: repo}.read},
		websearch.Exa{Key: piProviderKey{accounts: accounts, provider: store.ProviderExa, room: room, repo: repo}.read},
		websearch.Tavily{Key: piProviderKey{accounts: accounts, provider: store.ProviderTavily, room: room, repo: repo}.read},
	}
}

// piChatGPTCredential reads the ChatGPT sign-in that may run in the search's
// room and repository, through the same reader the voice service uses, so both
// follow one set of selection, validation and renewal rules.
type piChatGPTCredential struct {
	accounts   *harnessruntime.Accounts
	room, repo string
}

// Token is the access token and account ID of the ChatGPT sign-in, renewed when
// it is due.
func (c piChatGPTCredential) Token(ctx context.Context) (access, account string, err error) {
	grant, err := c.accounts.ChatGPTGrantDuringTurn(ctx, c.room, c.repo)
	if errors.Is(err, native.ErrNotSignedIn) {
		return "", "", websearch.ErrNotSignedIn
	}
	if err != nil {
		return "", "", err
	}
	return grant.Access, grant.Account, nil
}

// savedClaudeToken reads the access token of the Claude Code account that may
// run in the search's room and repository. Claude Code keeps its grant outside
// Pi, so this reads that account instead of a Pi login.
type savedClaudeToken struct {
	accounts   *harnessruntime.Accounts
	room, repo string
}

func (t savedClaudeToken) read(ctx context.Context) (string, error) {
	credential, err := t.accounts.SavedCredential(ctx, store.ProviderAnthropic, t.room, t.repo)
	if errors.Is(err, native.ErrNotSignedIn) {
		return "", websearch.ErrNotSignedIn
	}
	if err != nil {
		return "", err
	}
	token, err := native.ClaudeAccessToken(credential)
	if errors.Is(err, native.ErrSchema) || errors.Is(err, native.ErrNoIdentity) {
		// A saved login this release cannot read never answers a search, so
		// the chain treats it like a missing one and tries the next provider.
		return "", websearch.ErrNotSignedIn
	}
	return token, err
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

// piInferenceKey reads the key a Pi sign-in uses to call a provider that
// accepts either shape, such as Meta Muse: an OAuth sign-in keeps its minted
// inference key in the entry's access field, and a pasted key keeps it in key.
type piInferenceKey struct {
	accounts   *harnessruntime.Accounts
	provider   store.Provider
	room, repo string
}

func (k piInferenceKey) read(ctx context.Context) (string, error) {
	key, err := k.accounts.PiInferenceKey(ctx, k.provider, k.room, k.repo)
	if errors.Is(err, native.ErrNotSignedIn) {
		return "", websearch.ErrNotSignedIn
	}
	return key, err
}

// piAccessToken reads the access token of the Pi sign-in for a provider that may
// run in the search's room and repository, such as the xAI or Kimi Code one. The
// provider needs no account ID, so a Pi entry without one still answers.
type piAccessToken struct {
	accounts   *harnessruntime.Accounts
	provider   store.Provider
	room, repo string
}

func (t piAccessToken) read(ctx context.Context) (string, error) {
	token, err := t.accounts.PiAccessToken(ctx, t.provider, t.room, t.repo)
	if errors.Is(err, native.ErrNotSignedIn) {
		return "", websearch.ErrNotSignedIn
	}
	return token, err
}

package engine

import (
	"context"
	"errors"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
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
	results, err := s.webSearchChain().Search(ctx, websearch.Query{Text: in.Query, Limit: in.Limit})
	if err != nil {
		return callspec.WebSearchResponse{}, err
	}
	return callspec.WebSearchResponse{Results: results}, nil
}

// webSearchChain is the provider order for every search: the owner's ChatGPT
// sign-in first, because it answers with sources, then the Brave Search API
// when the owner saved a key for it.
func (s services) webSearchChain() websearch.Chain {
	accounts := s.runtime.Accounts
	return websearch.Chain{
		websearch.ChatGPT{Credential: piChatGPTCredential{accounts: accounts}},
		websearch.Brave{Key: piProviderKey{accounts: accounts, provider: store.ProviderBrave}.read},
	}
}

// piChatGPTCredential reads the active Pi ChatGPT login for each search.
type piChatGPTCredential struct {
	accounts *harnessruntime.Accounts
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
	access, account, err = c.accounts.PiChatGPTLogin(ctx)
	if errors.Is(err, native.ErrNotSignedIn) {
		return "", "", websearch.ErrNotSignedIn
	}
	if err != nil {
		return "", "", err
	}
	return access, account, nil
}

// piProviderKey reads the API key the active Pi account of a provider saved.
type piProviderKey struct {
	accounts *harnessruntime.Accounts
	provider store.Provider
}

func (k piProviderKey) read(ctx context.Context) (string, error) {
	key, err := k.accounts.PiProviderKey(ctx, k.provider)
	if errors.Is(err, native.ErrNotSignedIn) {
		return "", websearch.ErrNotSignedIn
	}
	return key, err
}

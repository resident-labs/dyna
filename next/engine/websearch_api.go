package engine

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/websearch"
)

func searchKeyProvider(provider contract.WebSearchProvider) bool {
	switch provider {
	case "muse", "brave", "exa", "tavily":
		return true
	}
	return false
}

func (s *Service) WebsearchSetKey(ctx context.Context, caller contract.Principal, req contract.WebsearchSetKeyRequest) (contract.WebsearchSetKeyResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.WebsearchSetKeyResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: "owner access required"}
	}
	if !searchKeyProvider(req.Provider) || strings.TrimSpace(req.Key) == "" {
		return contract.WebsearchSetKeyResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "provider and key are required"}
	}
	if err := s.store.SetSearchKey(ctx, string(req.Provider), req.Key); err != nil {
		return contract.WebsearchSetKeyResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "could not save search key"}
	}
	return contract.WebsearchSetKeyResponse{}, nil
}

func (s *Service) WebsearchRemoveKey(ctx context.Context, caller contract.Principal, req contract.WebsearchRemoveKeyRequest) (contract.WebsearchRemoveKeyResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.WebsearchRemoveKeyResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: "owner access required"}
	}
	if !searchKeyProvider(req.Provider) {
		return contract.WebsearchRemoveKeyResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "invalid search provider"}
	}
	if err := s.store.RemoveSearchKey(ctx, string(req.Provider)); err != nil {
		return contract.WebsearchRemoveKeyResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "could not remove search key"}
	}
	return contract.WebsearchRemoveKeyResponse{}, nil
}

type searchCandidate struct {
	provider websearch.Provider
	headroom float64
	recent   bool
}

// SetWebSearchProxy configures proxy-backed search without reading model tokens.
func (s *Service) SetWebSearchProxy(proxy *model.Proxy, quota *model.Quotas) {
	s.searchProxy, s.searchQuota = proxy, quota
}

func (s *Service) orderSearchCandidates(room contract.RoomID, candidates []searchCandidate, attempts []contract.WebSearchAttempt) []contract.WebSearchAttempt {
	s.searchMu.Lock()
	now := s.now()
	for i := range candidates {
		name := string(room) + "/" + candidates[i].provider.Name()
		if until, ok := s.searchFailures[name]; ok {
			if now.Before(until) {
				candidates[i].recent = true
				attempts = append(attempts, contract.WebSearchAttempt{Provider: contract.WebSearchProvider(candidates[i].provider.Name()), Reason: contract.WebSearchRecentFailure})
			} else {
				delete(s.searchFailures, name)
			}
		}
	}
	s.searchMu.Unlock()
	slices.SortStableFunc(candidates, func(a, b searchCandidate) int {
		if a.recent == b.recent {
			return 0
		}
		if a.recent {
			return 1
		}
		return -1
	})
	return attempts
}

func (s *Service) WebSearch(ctx context.Context, caller contract.Principal, req contract.WebSearchRequest) (contract.WebSearchResponse, *contract.Error) {
	if strings.TrimSpace(req.Query) == "" || req.Limit < 0 || req.Limit > websearch.MaxLimit {
		return contract.WebSearchResponse{}, &contract.Error{Code: contract.ErrInvalid, Message: "query is required and limit must be between 1 and 10"}
	}
	key := func(provider string) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			value, err := s.store.SearchKey(ctx, provider)
			if errors.Is(err, sql.ErrNoRows) {
				return "", websearch.ErrNotSignedIn
			}
			return value, err
		}
	}
	// A bounded deadline also protects calls that arrive without one.
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out := contract.WebSearchResponse{Results: []contract.WebSearchResult{}, Attempts: []contract.WebSearchAttempt{}}
	candidates := make([]searchCandidate, 0, 9)
	room := contract.RoomID("")
	if caller.Kind == contract.PrincipalSession {
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return out, &contract.Error{Code: contract.ErrForbidden, Message: "search caller unavailable"}
		}
		room = session.Room
	}
	if s.searchProxy != nil {
		accounts, err := s.store.Accounts(ctx)
		if err != nil {
			return out, &contract.Error{Code: contract.ErrUnavailable, Message: "search accounts unavailable"}
		}
		eligible, err := s.store.EligibleAccountProxyIDs(ctx, room)
		if err != nil {
			return out, &contract.Error{Code: contract.ErrUnavailable, Message: "search accounts unavailable"}
		}
		specs := []struct {
			name     contract.WebSearchProvider
			provider contract.AccountProvider
		}{
			{"chatgpt", contract.ProviderOpenAI}, {"claude", contract.ProviderAnthropic}, {"kimi", contract.ProviderKimi}, {"zai", contract.ProviderZAI}, {"xai", contract.ProviderXAI},
		}
		for _, spec := range specs {
			ids, headroom, reason := searchAccounts(accounts, eligible, spec.provider, s.searchQuota.CachedUsage)
			if reason != "" {
				out.Attempts = append(out.Attempts, contract.WebSearchAttempt{Provider: spec.name, Reason: reason})
				continue
			}
			client := s.searchProxy.SearchClient(ids)
			var provider websearch.Provider
			switch spec.name {
			case "chatgpt":
				provider = websearch.ChatGPT{BaseURL: "http://cliproxy/v1", Client: client}
			case "claude":
				provider = websearch.Claude{BaseURL: "http://cliproxy/v1", Client: client}
			case "xai":
				provider = websearch.XAI{BaseURL: "http://cliproxy/v1", Client: client}
			case "kimi", "zai":
				provider = websearch.ProxySearch{Provider: string(spec.name), Client: client}
			}
			candidates = append(candidates, searchCandidate{provider: provider, headroom: headroom})
		}
		slices.SortStableFunc(candidates, func(a, b searchCandidate) int {
			if a.headroom > b.headroom {
				return -1
			}
			if a.headroom < b.headroom {
				return 1
			}
			return 0
		})
	}
	for _, provider := range []websearch.Provider{websearch.Muse{Key: key("muse")}, websearch.Brave{BaseURL: s.searchBraveURL, Key: key("brave")}, websearch.Exa{Key: key("exa")}, websearch.Tavily{Key: key("tavily")}} {
		candidates = append(candidates, searchCandidate{provider: provider})
	}
	out.Attempts = s.orderSearchCandidates(room, candidates, out.Attempts)
	for _, candidate := range candidates {
		results, err := websearch.Attempt(ctx, candidate.provider, websearch.Query{Text: req.Query, Limit: req.Limit})
		if err == nil {
			out.Provider = contract.WebSearchProvider(candidate.provider.Name())
			for _, r := range results {
				out.Results = append(out.Results, contract.WebSearchResult{Title: r.Title, URL: r.URL, Snippet: r.Snippet, Provider: contract.WebSearchProvider(r.Provider)})
			}
			return out, nil
		}
		if ctx.Err() != nil {
			return out, &contract.Error{Code: contract.ErrUnavailable, Message: "search deadline reached"}
		}
		reason := contract.WebSearchFailed
		message := websearch.SafeMessage(err)
		if errors.Is(err, websearch.ErrNotSignedIn) {
			reason, message = contract.WebSearchNotSignedIn, ""
		} else if websearch.AuthFailure(err) {
			reason = contract.WebSearchNotSignedIn
		} else {
			s.searchMu.Lock()
			if s.searchFailures == nil {
				s.searchFailures = make(map[string]time.Time)
			} else if len(s.searchFailures) >= 256 {
				clear(s.searchFailures)
			}
			s.searchFailures[string(room)+"/"+candidate.provider.Name()] = s.now().Add(5 * time.Minute)
			s.searchMu.Unlock()
		}
		out.Attempts = append(out.Attempts, contract.WebSearchAttempt{Provider: contract.WebSearchProvider(candidate.provider.Name()), Reason: reason, Message: message})
	}
	return out, nil
}

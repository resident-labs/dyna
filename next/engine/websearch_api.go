package engine

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/websearch"
)

func searchKeyProvider(provider string) bool {
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
	if err := s.store.SetSearchKey(ctx, req.Provider, req.Key); err != nil {
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
	if err := s.store.RemoveSearchKey(ctx, req.Provider); err != nil {
		return contract.WebsearchRemoveKeyResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "could not remove search key"}
	}
	return contract.WebsearchRemoveKeyResponse{}, nil
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
	chain := websearch.Chain{websearch.Muse{Key: key("muse")}, websearch.Brave{Key: key("brave")}, websearch.Exa{Key: key("exa")}, websearch.Tavily{Key: key("tavily")}}
	results, err := chain.Search(ctx, websearch.Query{Text: req.Query, Limit: req.Limit})
	if err != nil {
		return contract.WebSearchResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: err.Error()}
	}
	out := contract.WebSearchResponse{Results: make([]contract.WebSearchResult, 0, len(results))}
	for _, r := range results {
		out.Results = append(out.Results, contract.WebSearchResult{Title: r.Title, URL: r.URL, Snippet: r.Snippet, Provider: r.Provider})
	}
	return out, nil
}

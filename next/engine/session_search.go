package engine

import (
	"context"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) SessionSearch(ctx context.Context, caller contract.Principal, req contract.SessionSearchRequest) (contract.SessionSearchResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.SessionSearchResponse{}, forbidden()
	}
	words := strings.Fields(req.Query)
	if len(req.Query) > 200 || len(words) == 0 || len(words) > 8 || req.Limit < 0 || req.Limit > 50 {
		return contract.SessionSearchResponse{}, invalid("invalid search query or range")
	}
	if req.Limit == 0 {
		req.Limit = 20
	}
	for i, word := range words {
		words[i] = `"` + strings.ReplaceAll(word, `"`, `""`) + `"*`
	}
	query := strings.Join(words, " AND ")
	result, err := s.store.SearchSessions(ctx, req, query)
	if err != nil {
		return result, failure(err)
	}
	return result, nil
}

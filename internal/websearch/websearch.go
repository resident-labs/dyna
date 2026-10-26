// Package websearch runs one web search through the provider that can answer
// it. Providers hold no state of their own: the engine supplies a credential
// reader for each, so the search tool never sees a token or a key.
package websearch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotSignedIn says one provider has no saved credential, so the chain skips
// it and tries the next provider. A provider that was used and failed returns
// another error.
var ErrNotSignedIn = errors.New("not signed in")

const (
	// DefaultLimit is how many results a search returns when it names none.
	DefaultLimit = 5
	// MaxLimit is the most results one search returns.
	MaxLimit = 10
	// maxSnippet is the longest snippet a result carries.
	maxSnippet = 300
	// minAttempt is the least time one provider gets when the caller set a
	// deadline, so a slow provider cannot spend the whole budget.
	minAttempt = 5 * time.Second
)

// Result is one search result.
type Result struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	Snippet  string `json:"snippet"`
	Provider string `json:"provider"`
}

// Query is one search request.
type Query struct {
	Text  string
	Limit int
}

// results is how many results the query asks for: its own limit, kept between
// one and MaxLimit.
func (q Query) results() int {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	return limit
}

// Provider is one web search source.
type Provider interface {
	// Name is the provider's name in a result and in an error.
	Name() string
	// Search answers a query. It returns ErrNotSignedIn when it has no
	// credential; the chain then tries the next provider.
	Search(ctx context.Context, q Query) ([]Result, error)
}

// Chain tries providers in order. It skips a provider that is not signed in
// and returns the first answer. When every provider fails, the error names each
// one with a short reason and carries no credential.
type Chain []Provider

// Search answers q from the first provider that can.
//
// Each provider may use only part of the caller's time, so a provider that
// stalls still leaves the providers after it enough time to answer. Without a
// caller deadline a provider is bounded by its own client timeout.
func (c Chain) Search(ctx context.Context, q Query) ([]Result, error) {
	if strings.TrimSpace(q.Text) == "" {
		return nil, errors.New("a search query is required")
	}
	if len(c) == 0 {
		return nil, errors.New("web search has no provider")
	}
	reasons := make([]string, 0, len(c))
	for _, provider := range c {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		try, cancel := attemptContext(ctx)
		results, err := provider.Search(try, q)
		cancel()
		if err == nil {
			return results, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			// The caller's own deadline is gone, so no provider after this one
			// can answer either.
			return nil, ctxErr
		}
		if errors.Is(err, ErrNotSignedIn) {
			reasons = append(reasons, provider.Name()+" is not signed in")
			continue
		}
		reasons = append(reasons, provider.Name()+" failed: "+oneLine(err))
	}
	return nil, errors.New("web search found no provider: " + strings.Join(reasons, "; "))
}

// attemptContext bounds one provider's attempt to attemptBudget of the time the
// caller left. Without a caller deadline a provider is bounded by its own
// client timeout.
func attemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return ctx, func() {}
	}
	left := time.Until(deadline)
	if left <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, attemptBudget(left))
}

// attemptBudget is how long one provider may run when the caller has left this
// much time: half of it, so the providers after it keep the other half. Half
// would cut a short budget off before a provider starts, so it also never gives
// less than minAttempt.
func attemptBudget(left time.Duration) time.Duration {
	attempt := left / 2
	if attempt < minAttempt {
		attempt = min(minAttempt, left)
	}
	return attempt
}

// oneLine keeps a provider's reason on the one line the tool reports.
func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}

// capped returns results limited to q, tagged with the provider that found them.
func capped(provider string, q Query, results []Result) []Result {
	limit := q.results()
	if len(results) > limit {
		results = results[:limit]
	}
	for i := range results {
		results[i].Provider = provider
	}
	return results
}

// text folds a snippet onto one line of at most maxSnippet characters.
func text(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > maxSnippet {
		s = strings.TrimSpace(string(runes[:maxSnippet])) + "…"
	}
	return s
}

// statusError is the failure of a provider request that answered.
func statusError(provider string, status int) error {
	return fmt.Errorf("%s answered HTTP %d", provider, status)
}

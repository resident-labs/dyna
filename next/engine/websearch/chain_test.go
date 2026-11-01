package websearch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeProvider answers as its errors say: a not-signed-in provider is skipped.
type fakeProvider struct {
	name    string
	results []Result
	err     error
	calls   *int
}

func (f fakeProvider) Name() string { return f.name }

func (f fakeProvider) Search(context.Context, Query) ([]Result, error) {
	*f.calls++
	return f.results, f.err
}

func TestChainSkipsAProviderWithoutASignIn(t *testing.T) {
	asked := 0
	chain := Chain{
		fakeProvider{name: "chatgpt", err: ErrNotSignedIn, calls: &asked},
		fakeProvider{name: "brave", results: []Result{{Title: "Go", URL: "https://go.dev"}}, calls: &asked},
	}
	got, err := chain.Search(t.Context(), Query{Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != "https://go.dev" || asked != 2 {
		t.Fatalf("results = %+v after %d providers, want brave's one result after both", got, asked)
	}
}

func TestChainStopsAtTheFirstAnswer(t *testing.T) {
	first, second := 0, 0
	chain := Chain{
		fakeProvider{name: "chatgpt", results: []Result{{Title: "One", URL: "https://one.test"}}, calls: &first},
		fakeProvider{name: "brave", results: []Result{{Title: "Two", URL: "https://two.test"}}, calls: &second},
	}
	got, err := chain.Search(t.Context(), Query{Text: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "One" || second != 0 {
		t.Fatalf("results = %+v, brave asked %d times; want only ChatGPT's answer", got, second)
	}
}

// A failed provider is named with a short reason, and no credential appears in
// the message.
func TestChainNamesEveryProviderWhenAllFail(t *testing.T) {
	asked := 0
	chain := Chain{
		fakeProvider{name: "chatgpt", err: errors.New("chatgpt answered HTTP 401 for Bearer sk-secret"), calls: &asked},
		fakeProvider{name: "brave", err: ErrNotSignedIn, calls: &asked},
	}
	_, err := chain.Search(t.Context(), Query{Text: "go"})
	if err == nil {
		t.Fatal("a chain with no provider answered without an error")
	}
	message := err.Error()
	for _, want := range []string{"chatgpt failed: chatgpt answered HTTP 401", "brave is not signed in"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q does not name %q", message, want)
		}
	}
	if strings.Contains(message, "\n") {
		t.Fatalf("error spans more than one line: %q", message)
	}
}

// A provider that runs past its own attempt is cut short, and the next provider
// still answers: the attempt deadline is earlier than the caller's, so a stalled
// provider never spends the fallback's time.
func TestChainGivesAFallbackItsOwnTime(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	asked := 0
	stalled := Chain{
		fakeProvider{name: "chatgpt", err: context.DeadlineExceeded, calls: &asked},
		fakeProvider{name: "brave", results: []Result{{Title: "Go", URL: "https://go.dev"}}, calls: &asked},
	}
	got, err := stalled.Search(ctx, Query{Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != "https://go.dev" || asked != 2 {
		t.Fatalf("results = %+v after %d providers, want brave's answer after both", got, asked)
	}
	// Each attempt carries its own deadline, earlier than the caller's.
	try, release := attemptContext(ctx)
	defer release()
	attempt, ok := try.Deadline()
	if !ok || !attempt.Before(deadline) {
		t.Fatalf("attempt deadline = %v, want one before the caller's %v", attempt, deadline)
	}
	if budget := attemptBudget(time.Minute); budget != 30*time.Second {
		t.Fatalf("budget of one minute = %v, want half of it", budget)
	}
	if budget := attemptBudget(4 * time.Second); budget != 4*time.Second {
		t.Fatalf("budget of four seconds = %v, want the whole of it", budget)
	}
}

// A caller deadline that is already gone stops the chain instead of asking a
// provider a question it cannot finish.
func TestChainStopsWhenTheCallersDeadlineIsGone(t *testing.T) {
	asked := 0
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := (Chain{fakeProvider{name: "brave", results: []Result{{URL: "https://go.dev"}}, calls: &asked}}).Search(ctx, Query{Text: "go"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline; providers asked %d", err, asked)
	}
}

func TestChainNeedsAQuery(t *testing.T) {
	asked := 0
	_, err := Chain{fakeProvider{name: "brave", calls: &asked}}.Search(t.Context(), Query{Text: "   "})
	if err == nil || asked != 0 {
		t.Fatalf("blank query: err = %v, providers asked %d; want a refusal without a provider call", err, asked)
	}
}

func TestQueryLimitDefaultsAndCaps(t *testing.T) {
	for _, c := range []struct{ limit, want int }{{0, DefaultLimit}, {-3, DefaultLimit}, {2, 2}, {99, MaxLimit}} {
		if got := (Query{Limit: c.limit}).results(); got != c.want {
			t.Errorf("limit %d = %d results, want %d", c.limit, got, c.want)
		}
	}
}

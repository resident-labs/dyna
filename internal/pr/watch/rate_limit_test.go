package watch

import (
	"bufio"
	"context"
	"dyna-agent/internal/pr/state"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestConditionalPaginationUsesEachPageValidatorAndCachedLink(t *testing.T) {
	e := newEnv(t)
	ref := Ref{"github.com", "owner/repo", 17}
	first := ref.path("/comments?per_page=100")
	second := ref.path("/comments?per_page=100&page=2")
	e.f.Raw = map[string]string{
		first:  "HTTP/2.0 200 OK\r\nETag: \"one\"\r\nLink: <https://api.github.com/" + second + ">; rel=\"next\"\r\n\r\n[{\"id\":1}]",
		second: "HTTP/2.0 200 OK\r\nETag: \"two\"\r\n\r\n[{\"id\":2}]",
	}
	e.write()
	cache := &requestCache{entries: map[string]cachedResponse{}, used: map[string]cachedResponse{}, rates: map[string]RateLimit{}, now: e.s.now}
	gh := e.s.GH
	gh.requests = cache
	var pages [][]Comment
	if err := gh.pages(context.Background(), ref, first, &pages); err != nil {
		t.Fatal(err)
	}
	cache.entries, cache.used = cache.used, map[string]cachedResponse{}
	e.f.Raw[first] = "HTTP/2.0 304 Not Modified\r\n\r\n"
	e.f.Raw[second] = "HTTP/2.0 200 OK\r\nETag: \"three\"\r\n\r\n[{\"id\":3}]"
	e.write()
	if err := gh.pages(context.Background(), ref, first, &pages); err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0][0].ID != 1 || pages[1][0].ID != 3 {
		t.Fatalf("pages = %+v", pages)
	}
	calls, _ := os.ReadFile(e.calls)
	if !strings.Contains(string(calls), first+" --include -H If-None-Match: \"one\"") || !strings.Contains(string(calls), second+" --include -H If-None-Match: \"two\"") {
		t.Fatalf("validators mixed: %s", calls)
	}
	// A changed first page without a next link removes the old second page.
	e.f.Raw[first] = "HTTP/2.0 200 OK\r\nETag: \"four\"\r\n\r\n[]"
	e.write()
	if err := gh.pages(context.Background(), ref, first, &pages); err != nil || len(pages) != 1 {
		t.Fatalf("removed page: %v %+v", err, pages)
	}
}

func TestPollingKeepsQuietPRsAtOneMinute(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	snap := Snapshot{Checks: []Check{actionsCheck(Check{ID: 1, Status: "completed", Conclusion: "success"})}}
	p := Polling{}
	observe(&p, now, snap, false)
	if p.IntervalMinutes != 1 {
		t.Fatal(p)
	}
	for minute := 1; minute < 10; minute++ {
		observe(&p, now.Add(time.Duration(minute)*time.Minute), snap, false)
		if p.IntervalMinutes != 1 {
			t.Fatal(p)
		}
	}
	at := now.Add(10 * time.Minute)
	for _, want := range []int{1, 1, 1, 1, 1} {
		observe(&p, at, snap, false)
		if p.IntervalMinutes != want || !p.Next.Equal(at.Add(time.Duration(want)*time.Minute)) {
			t.Fatalf("want %d: %+v", want, p)
		}
		at = p.Next
	}
	snap.Pull.Updated = "changed"
	observe(&p, at, snap, false)
	if p.IntervalMinutes != 1 {
		t.Fatalf("change did not reset: %+v", p)
	}
	snap.Checks[0].Status = "in_progress"
	observe(&p, at.Add(time.Hour), snap, false)
	if p.IntervalMinutes != 1 {
		t.Fatalf("pending backed off: %+v", p)
	}
	observe(&p, at.Add(2*time.Hour), snap, true)
	if p.IntervalMinutes != 5 {
		t.Fatalf("reserve did not override pending: %+v", p)
	}
}

func TestTickSkipsUntilDueButFlushesOutbox(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	e.tick()
	before, _ := os.ReadFile(e.calls)
	if err := e.s.Store.update(func(st *State) error { notice(st.Records[r.Key], "", "pending delivery", "test"); return nil }); err != nil {
		t.Fatal(err)
	}
	count := len(e.sent)
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(e.calls)
	newCalls := strings.TrimPrefix(string(after), string(before))
	if strings.Count(newCalls, "api ") != 1 || !strings.Contains(newCalls, "graphql") || len(e.sent) != count+1 {
		t.Fatalf("due skip or outbox failed")
	}
}

func TestRateLimitPausesAllPRsAndSurvivesRestart(t *testing.T) {
	for _, status := range []int{403, 429} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			e := newEnv(t)
			r := e.track()
			if err := e.s.Store.update(func(st *State) error {
				other := *st.Records[r.Key]
				other.Key = "z"
				st.Records[other.Key] = &other
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			reset := e.clock.Add(20 * time.Minute)
			e.f.Raw = map[string]string{"graphql": fmt.Sprintf("HTTP/2.0 %d Rate Limited\r\nX-Ratelimit-Remaining: 0\r\nX-Ratelimit-Reset: %d\r\n\r\n{\"message\":\"API rate limit exceeded\"}", status, reset.Unix())}
			e.write()
			if _, err := e.s.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "polling paused") {
				t.Fatalf("rate error: %v", err)
			}
			before, _ := os.ReadFile(e.calls)
			e.clock = e.clock.Add(time.Minute)
			if _, err := e.s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(e.calls)
			if string(before) != string(after) {
				t.Fatal("polled during shared pause")
			}
			details, err := e.s.Show(r.Key)
			if err != nil || !details.RateLimit.PauseUntil.After(reset) {
				t.Fatalf("pause not shown: %+v %v", details, err)
			}
			e.clock = reset.Add(2 * time.Second)
			e.f.Raw = nil
			e.write()
			if _, err := e.s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !e.record().RateLimit.PauseUntil.IsZero() {
				t.Fatal("pause did not clear")
			}
		})
	}
}

func TestSecondaryLimitRetryAndReserve(t *testing.T) {
	e := newEnv(t)
	e.track()
	path := "graphql"
	e.f.Raw = map[string]string{path: "HTTP/2.0 403 Forbidden\r\nRetry-After: 180\r\n\r\n{\"message\":\"You have exceeded a secondary rate limit\"}"}
	e.write()
	if _, err := e.s.Tick(context.Background()); err == nil {
		t.Fatal("missing secondary error")
	}
	if got := e.record().RateLimit.PauseUntil; !got.Equal(e.clock.Add(3 * time.Minute)) {
		t.Fatalf("retry time: %v", got)
	}
	e.clock = e.clock.Add(4 * time.Minute)
	e.f.Raw = nil
	e.f.Headers = map[string]string{"X-Ratelimit-Remaining": "499", "X-Ratelimit-Reset": strconv.FormatInt(e.clock.Add(time.Hour).Unix(), 10)}
	e.write()
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.record().Polling.IntervalMinutes != 5 {
		t.Fatal("reserve did not slow polling")
	}
	b, _ := json.Marshal(e.record())
	if strings.Contains(string(b), "httpResponses") {
		t.Fatal("raw responses leaked into PR output")
	}
}

func TestSecondaryLimitBackoffAndOrdinaryForbidden(t *testing.T) {
	e := newEnv(t)
	path := "repos/owner/repo/pulls/17"
	ref := Ref{"github.com", "owner/repo", 17}
	rates := map[string]RateLimit{}
	gh := e.s.GH
	gh.requests = &requestCache{entries: map[string]cachedResponse{}, used: map[string]cachedResponse{}, rates: rates, now: e.s.now}
	e.f.Raw = map[string]string{path: "HTTP/2.0 403 Forbidden\r\n\r\n{\"message\":\"Resource not accessible by integration\"}"}
	e.write()
	if _, _, err := gh.request(context.Background(), ref, path); err == nil || !rates[(RateLimitScope{Host: ref.Host, Resource: "core"}).Key()].PauseUntil.IsZero() {
		t.Fatalf("ordinary forbidden: %+v %v", rates, err)
	}
	e.f.Raw[path] = "HTTP/2.0 403 Forbidden\r\n\r\n{\"message\":\"You have exceeded a secondary rate limit\"}"
	e.write()
	for _, minutes := range []int{1, 2, 4} {
		start := e.clock
		if _, _, err := gh.request(context.Background(), ref, path); err == nil {
			t.Fatal("missing rate error")
		}
		rate := rates[(RateLimitScope{Host: ref.Host, Resource: "core"}).Key()]
		if !rate.PauseUntil.Equal(start.Add(time.Duration(minutes) * time.Minute)) {
			t.Fatalf("delay %d: %+v", minutes, rate)
		}
		e.clock = rate.PauseUntil.Add(time.Second)
	}
}

func TestCachedResponseClearsExpiredPause(t *testing.T) {
	e := newEnv(t)
	path := "repos/owner/repo/pulls/17"
	ref := Ref{"github.com", "owner/repo", 17}
	scope := RateLimitScope{Host: "github.com", Resource: "core"}
	rates := map[string]RateLimit{scope.Key(): {PauseUntil: e.clock.Add(-time.Minute), Reason: "GitHub rate limit"}}
	gh := e.s.GH
	gh.requests = &requestCache{entries: map[string]cachedResponse{"github.com/" + path: {ETag: `"one"`, Body: json.RawMessage(`{}`)}}, used: map[string]cachedResponse{}, rates: rates, now: e.s.now}
	e.f.Raw = map[string]string{path: "HTTP/2.0 304 Not Modified\r\n\r\n"}
	e.write()
	if _, _, err := gh.request(context.Background(), ref, path); err != nil {
		t.Fatal(err)
	}
	rate := rates[scope.Key()]
	if !rate.PauseUntil.IsZero() || rate.Reason != "" {
		t.Fatalf("stale pause: %+v", rate)
	}
}

func TestSuccessfulServiceCallResumesAllWatchedPRsAfterOutage(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	accountGH(t, e)
	until := e.clock.Add(time.Hour)
	zero := 0
	if err := e.s.Store.update(func(st *State) error {
		scope := state.RateLimitScope{Host: "github.com", Account: "fixture", Resource: "core"}
		st.RateLimits[scope.Key()] = RateLimit{Remaining: &zero, Reset: until, PauseUntil: until, PauseScope: scope}
		for _, key := range []string{r.Key, "other", "stopped"} {
			clone := *st.Records[r.Key]
			clone.QuotaAccount = "fixture"
			clone.Key = key
			clone.Watching = key != "stopped"
			clone.CIPaused = true
			clone.Polling.Next = until
			clone.Polling.FullChecked = e.clock
			st.Records[key] = &clone
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	body, _ := json.Marshal(e.f.Pull)
	e.f.Raw = map[string]string{refRecord(r).path(""): fmt.Sprintf("HTTP/2.0 200 OK\r\nX-Ratelimit-Remaining: 4995\r\nX-Ratelimit-Reset: %d\r\n\r\n%s", until.Unix(), body)}
	e.write()
	if _, err := e.s.github(r.RoomID).Pull(t.Context(), refRecord(r)); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !st.RateLimit.PauseUntil.IsZero() {
		t.Fatalf("global pause remains: %+v", st.RateLimit)
	}
	for key, record := range st.Records {
		if record.Watching {
			if !record.Polling.Next.IsZero() || !record.Polling.FullChecked.IsZero() || !record.RateLimit.PauseUntil.IsZero() {
				t.Fatalf("%s did not resume full polling: %+v", key, record.Polling)
			}
			if !record.CIPaused {
				t.Fatal("recovery cleared the separate CI notice pause")
			}
		} else if !record.Polling.Next.Equal(until) {
			t.Fatal("recovery rescheduled a stopped watch")
		}
	}
	e.f.Raw = nil
	e.write()
	if _, err := e.s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.record().Head; got != e.f.Pull.Head.SHA {
		t.Fatalf("stale head after recovery: %s", got)
	}
}

func TestExplicitCallWithoutAvailableQuotaKeepsPollingPause(t *testing.T) {
	e := newEnv(t)
	accountGH(t, e)
	r := e.track()
	until := e.clock.Add(time.Hour)
	if err := e.s.Store.update(func(st *State) error {
		st.RateLimits = map[string]RateLimit{(RateLimitScope{Host: "github.com", Account: "fixture", Resource: "core"}).Key(): {PauseUntil: until}, (RateLimitScope{Host: "github.com", Account: "fixture", Resource: "graphql"}).Key(): {PauseUntil: until}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(e.f.Pull)
	e.f.Raw = map[string]string{refRecord(r).path(""): fmt.Sprintf("HTTP/2.0 200 OK\r\nX-Ratelimit-Remaining: 0\r\n\r\n%s", body)}
	e.write()
	if _, err := e.s.github(r.RoomID).Pull(t.Context(), refRecord(r)); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !st.RateLimit.PauseUntil.Equal(until) {
		t.Fatal("a call that exhausted quota cleared the pause")
	}
}

func TestAccountGHRepositoryMetadataAndPullResponses(t *testing.T) {
	e := newEnv(t)
	e.write()
	accountGH(t, e)
	for _, endpoint := range []string{"repos/owner/repo", "repos/owner/repo/pulls/17"} {
		for _, include := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/include=%t", endpoint, include), func(t *testing.T) {
				args := []string{"api", "--hostname", "github.com", endpoint}
				if include {
					args = append(args, "--include")
				}
				body, err := exec.CommandContext(t.Context(), e.s.GH.Executable, args...).Output()
				if err != nil {
					t.Fatal(err)
				}
				decoder := json.NewDecoder(strings.NewReader(string(body)))
				if include {
					response, err := http.ReadResponse(bufio.NewReader(strings.NewReader(string(body))), nil)
					if err != nil {
						t.Fatalf("invalid included response: %v", err)
					}
					defer response.Body.Close()
					if response.StatusCode != http.StatusOK {
						t.Fatalf("status = %d", response.StatusCode)
					}
					decoder = json.NewDecoder(response.Body)
				}
				var got struct {
					Permissions struct {
						Push bool `json:"push"`
					} `json:"permissions"`
					Pull
				}
				if err := decoder.Decode(&got); err != nil {
					t.Fatal(err)
				}
				if endpoint == "repos/owner/repo" {
					if !got.Permissions.Push || got.Number != 0 {
						t.Fatalf("repository response = %+v", got)
					}
				} else if got.Number != e.f.Pull.Number || got.Head.SHA != e.f.Pull.Head.SHA || got.Permissions.Push {
					t.Fatalf("pull response = %+v", got)
				}
			})
		}
	}
}

func accountGH(t *testing.T, e *testEnv) {
	t.Helper()
	original := e.s.GH.Executable
	wrapper := original + "-account"
	script := "#!/bin/sh\ncase \"$1 $2\" in\n" +
		" 'auth status') echo '{\"hosts\":{\"github.com\":[{\"login\":\"fixture\"}]}}'; exit 0;;\n" +
		" 'auth token') echo fixture-token; exit 0;;\nesac\n" +
		"metadata=false; include=false\n" +
		"for arg do case \"$arg\" in repos/owner/repo) metadata=true;; --include) include=true;; esac; done\n" +
		"if [ \"$metadata\" = true ]; then\n" +
		" if [ \"$include\" = true ]; then printf 'HTTP/2.0 200 OK\\r\\nContent-Type: application/json\\r\\n\\r\\n'; fi\n" +
		" echo '{\"permissions\":{\"push\":true}}'; exit 0\nfi\n" +
		"exec '" + strings.ReplaceAll(original, "'", "'\\''") + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	e.s.GH.Executable = wrapper
	e.s.GH.run = nil
}

func TestRESTSuccessDoesNotRecoverGraphQLPause(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	accountGH(t, e)
	until := e.clock.Add(time.Hour)
	scope := state.RateLimitScope{Host: "github.com", Account: "fixture", Resource: "graphql"}
	if err := e.s.Store.update(func(st *State) error {
		st.RateLimits[scope.Key()] = RateLimit{PauseUntil: until, PauseScope: scope}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(e.f.Pull)
	e.f.Raw = map[string]string{refRecord(r).path(""): fmt.Sprintf("HTTP/2.0 200 OK\r\nX-Ratelimit-Remaining: 4995\r\nX-Ratelimit-Resource: core\r\n\r\n%s", body)}
	e.write()
	if _, err := e.s.github(r.RoomID).Pull(t.Context(), refRecord(r)); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !st.RateLimit.PauseUntil.Equal(until) {
		t.Fatal("REST quota cleared a GraphQL pause")
	}
}

func TestRecoveryRequiresOriginalQuotaScope(t *testing.T) {
	for _, difference := range []string{"host", "account", "resource", "unknown"} {
		t.Run(difference, func(t *testing.T) {
			e := newEnv(t)
			e.track()
			until := e.clock.Add(time.Hour)
			original := state.RateLimitScope{Host: "github.com", Account: "alice", Resource: "graphql"}
			if err := e.s.Store.update(func(st *State) error {
				st.RateLimits[original.Key()] = RateLimit{PauseUntil: until, PauseScope: original}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			changed := original
			switch difference {
			case "host":
				changed.Host = "other.example"
			case "account":
				changed.Account = "bob"
			case "resource":
				changed.Resource = "core"
			case "unknown":
				changed.Account = ""
			}
			quota := 4995
			if err := e.s.recoverPolling(RateLimit{Remaining: &quota}, changed); err != nil {
				t.Fatal(err)
			}
			st, err := e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if !st.RateLimit.PauseUntil.Equal(until) {
				t.Fatalf("%s cleared another scope's pause", difference)
			}
		})
	}
}

func TestRecoverySpreadsFullPollsAcrossMinutes(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	scope := state.RateLimitScope{Host: "github.com", Account: "fixture", Resource: "core"}
	if err := e.s.Store.update(func(st *State) error {
		st.RateLimits[scope.Key()] = RateLimit{PauseUntil: e.clock.Add(time.Hour), PauseScope: scope}
		st.Records[r.Key].QuotaAccount = "fixture"
		for i := 0; i < 29; i++ {
			clone := *st.Records[r.Key]
			clone.QuotaAccount = "fixture"
			clone.Key = fmt.Sprintf("recovery-%02d", i)
			clone.Polling.Next = e.clock.Add(time.Hour)
			clone.Polling.FullChecked = e.clock
			st.Records[clone.Key] = &clone
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	available := 4995
	if err := e.s.recoverPolling(RateLimit{Remaining: &available}, scope); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	for minute, want := range []int{10, 20, 30} {
		due := 0
		for _, r := range st.Records {
			if pollDue(st, r, e.clock.Add(time.Duration(minute)*time.Minute)) {
				due++
			}
			if !r.Polling.FullChecked.IsZero() {
				t.Fatal("recovery did not require a full poll")
			}
		}
		if due != want {
			t.Fatalf("minute %d: %d due, want %d", minute, due, want)
		}
	}
}

func TestConditionalRequestsSurviveRestartAndPreserveEvents(t *testing.T) {
	e := newEnv(t)
	// Pending CI keeps all conditional endpoints active, including comments.
	e.f.Checks[0].Status = "in_progress"
	e.write()
	e.track()
	e.tick()
	before := len(e.sent)
	// Recreate the service to ensure validators come from durable state.
	e.s.GH = GitHub{Executable: e.s.GH.Executable, run: e.s.GH.run}
	e.tick()
	calls, _ := os.ReadFile(e.calls)
	if count := strings.Count(string(calls), "If-None-Match:"); count != 6 {
		t.Fatalf("conditional calls = %d, want 6: %s", count, calls)
	}
	if len(e.sent) != before {
		t.Fatal("304 replay sent duplicate notices")
	}
	// The pull metadata remains unchanged, but all kinds of comments can change.
	// GitHub does not document updated_at propagation for every comment action.
	for i, kind := range []string{"reviews", "comments", "issue-comments"} {
		e.f.Comments = append(e.f.Comments, Comment{ID: int64(i + 1), Kind: kind, Body: "Please fix this", User: User{Login: "human"}, State: "COMMENTED"})
		e.tick()
		if len(e.record().Seen) != i+1 {
			t.Fatalf("missed %s with unchanged pull", kind)
		}
	}
}

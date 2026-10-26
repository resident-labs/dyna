package watch

import (
	"context"
	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestQuotaResourcesKeepIndependentWindows(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	rates := map[string]RateLimit{}
	gh := GitHub{run: func(ctx context.Context, cmd *exec.Cmd) error {
		remaining, reset, resource := 100, now.Add(time.Hour), "core"
		if strings.Contains(strings.Join(cmd.Args, " "), "graphql") {
			remaining, reset, resource = 4000, now.Add(2*time.Hour), "graphql"
		}
		fmt.Fprintf(cmd.Stdout, "HTTP/2.0 200 OK\r\nX-RateLimit-Resource: %s\r\nX-RateLimit-Remaining: %d\r\nX-RateLimit-Reset: %d\r\n\r\n{}", resource, remaining, reset.Unix())
		return nil
	}}
	gh.requests = &requestCache{rates: rates, used: map[string]cachedResponse{}, now: func() time.Time { return now }}
	ref := Ref{Host: "github.com", Repo: "owner/repo"}
	for _, path := range []string{"repos/owner/repo", "graphql", "repos/owner/repo"} {
		if _, _, err := gh.request(context.Background(), ref, path); err != nil {
			t.Fatal(err)
		}
	}
	rate := rates[(RateLimitScope{Host: "github.com", Resource: "core"}).Key()]
	if rate.Remaining == nil || *rate.Remaining != 100 || !rate.Reset.Equal(now.Add(time.Hour)) {
		t.Fatalf("GraphQL overwrote the REST window: %+v", rate)
	}
}

// The fixture follows the real credential selection and request path. Probes
// select Alice or Bob by repository; the API process sees that account's token.
func TestQuotaScopesSurviveRestartAndRecoverSelectively(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	rooms := room.DefaultStore()
	scopes := []RateLimitScope{
		{Host: "github.com", Account: "alice", Resource: "core"},
		{Host: "github.com", Account: "alice", Resource: "graphql"},
		{Host: "github.com", Account: "bob", Resource: "core"},
		{Host: "git.example", Account: "alice", Resource: "core"},
	}
	roomIDs := map[string]string{}
	for i, scope := range scopes {
		key := scope.Host + "/" + scope.Account
		if roomIDs[key] != "" {
			continue
		}
		rec, err := rooms.Create(room.CreateInput{Name: fmt.Sprintf("scope-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		rec = roomfixture.Repository(t, rooms, rec.ID, scope.Host, "owner/"+scope.Account, "read-only")
		roomIDs[key] = rec.ID
	}
	responsePath := func(scope RateLimitScope) string {
		return filepath.Join(dir, scope.Host+"-"+scope.Account+"-"+scope.Resource)
	}
	writeReply := func(scope RateLimitScope, status, remaining int, reset time.Time) {
		t.Helper()
		body := fmt.Sprintf("HTTP/2.0 %d Status\r\nX-RateLimit-Resource: %s\r\nX-RateLimit-Remaining: %d\r\nX-RateLimit-Reset: %d\r\n\r\n{}", status, scope.Resource, remaining, reset.Unix())
		if err := os.WriteFile(responsePath(scope), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
case "$1 $2" in
 'auth status') echo '{"hosts":{"github.com":[{"login":"alice"},{"login":"bob"}],"git.example":[{"login":"alice"},{"login":"bob"}]}}'; exit 0;;
 'auth token') case "$*" in *alice*) echo alice;; *bob*) echo bob;; esac; exit 0;;
esac
case "$4" in
 repos/owner/alice|repos/owner/bob)
  case "$GH_TOKEN:$4" in alice:repos/owner/alice|bob:repos/owner/bob) echo '{}'; exit 0;; esac
  echo 'gh: Not Found (HTTP 404)' >&2; exit 1;;
esac
resource=core
[ "$4" = graphql ] && resource=graphql
cat '` + dir + `/'"$3-$GH_TOKEN-$resource"
`
	exe := filepath.Join(dir, "gh")
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: &Store{Root: filepath.Join(dir, "store")}, GH: GitHub{Executable: exe, Rooms: rooms}, Now: func() time.Time { return now }}
	rates := map[string]RateLimit{}
	cache := &requestCache{rates: rates, used: map[string]cachedResponse{}, now: service.now}
	call := func(scope RateLimitScope, explicit bool) error {
		gh := service.github(roomIDs[scope.Host+"/"+scope.Account])
		if !explicit {
			gh.requests = cache
		}
		path := "repos/owner/" + scope.Account + "/pulls/1"
		if scope.Resource == "graphql" {
			path = "graphql"
			_, _, err := gh.requestMethod(t.Context(), Ref{Host: scope.Host, Repo: "owner/" + scope.Account}, path, "POST", "-f", "query=query { viewer { login } }")
			return err
		}
		_, _, err := gh.request(t.Context(), Ref{Host: scope.Host, Repo: "owner/" + scope.Account, Number: 1}, path)
		return err
	}
	counts := []int{100, 4000, 4900, 4800}
	for i, scope := range scopes {
		writeReply(scope, 200, counts[i], now.Add(time.Duration(i+1)*time.Hour))
		if err := call(scope, false); err != nil {
			t.Fatal(err)
		}
	}
	if len(rates) != 4 {
		t.Fatalf("scopes=%+v", rates)
	}
	for i, scope := range scopes {
		rate := rates[scope.Key()]
		if rate.Remaining == nil || *rate.Remaining != counts[i] || !rate.Reset.Equal(now.Add(time.Duration(i+1)*time.Hour)) {
			t.Fatalf("scope %v: %+v", scope, rate)
		}
	}
	// A newer reply with a larger count in one window cannot restore spent quota.
	writeReply(scopes[0], 200, 4500, now.Add(time.Hour))
	if err := call(scopes[0], false); err != nil {
		t.Fatal(err)
	}
	if *rates[scopes[0].Key()].Remaining != 100 {
		t.Fatal("same-window quota increased")
	}
	until := now.Add(time.Hour)
	for _, scope := range scopes[:2] {
		writeReply(scope, 429, 0, rates[scope.Key()].Reset)
		if err := call(scope, false); err == nil {
			t.Fatal("missing pause")
		}
	}
	if err := service.Store.update(func(st *State) error {
		for i, scope := range []RateLimitScope{scopes[0], scopes[2], scopes[3]} {
			st.Records[fmt.Sprint(i)] = &Record{Key: fmt.Sprint(i), Host: scope.Host, Repo: "owner/" + scope.Account, QuotaAccount: scope.Account, State: "open", Watching: true, Polling: Polling{Next: until, FullChecked: now}}
		}
		applyRateLimits(st, rates, now)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Start again from the saved entries. Only Alice on github.com is paused.
	st, err := service.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	cache = &requestCache{rates: st.RateLimits, used: map[string]cachedResponse{}, now: service.now}
	for i, scope := range scopes {
		err := call(scope, false)
		if (i < 2) != (err != nil) {
			t.Fatalf("scope %v pause: %v", scope, err)
		}
	}
	writeReply(scopes[0], 200, 4900, now.Add(2*time.Hour))
	if err := call(scopes[0], true); err != nil {
		t.Fatal(err)
	}
	st, err = service.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !st.RateLimits[scopes[0].Key()].PauseUntil.IsZero() || st.RateLimits[scopes[1].Key()].PauseUntil.IsZero() {
		t.Fatal("REST recovery changed GraphQL pause")
	}
	if !st.Records["0"].Polling.Next.Equal(until) {
		t.Fatal("REST recovery bypassed GraphQL pause")
	}
	for _, key := range []string{"1", "2"} {
		if st.Records[key].Polling.IntervalMinutes != 0 {
			t.Fatal("another scope consumed the reserve")
		}
		if !st.Records[key].Polling.FullChecked.Equal(now) {
			t.Fatal("recovery rescheduled another account or host")
		}
	}
	writeReply(scopes[1], 200, 4800, now.Add(3*time.Hour))
	if err := call(scopes[1], true); err != nil {
		t.Fatal(err)
	}
	st, err = service.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Records["0"].Polling.Next.IsZero() || !st.Records["0"].Polling.FullChecked.IsZero() {
		t.Fatal("matching scope did not recover")
	}
	// The stored index contains entries, never the derived mixed display.
	db, err := database.Open(filepath.Dir(service.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRow("SELECT body FROM pr_state WHERE id=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var index map[string]json.RawMessage
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	if _, found := index["rateLimit"]; found {
		t.Fatal("saved derived mixed quota")
	}
}

func TestQuotaOldSnapshotsCannotClearNewPauses(t *testing.T) {
	now := time.Now()
	scope := RateLimitScope{Host: "github.com", Account: "alice", Resource: "core"}
	until := now.Add(time.Hour)
	st := State{RateLimits: map[string]RateLimit{scope.Key(): {ObservedAt: now, PauseUntil: until}}}
	applyRateLimits(&st, map[string]RateLimit{scope.Key(): {ObservedAt: now.Add(-time.Second)}}, now)
	if !st.RateLimits[scope.Key()].PauseUntil.Equal(until) {
		t.Fatal("old snapshot cleared the pause")
	}
}

func TestWorkflowCapabilityRequestsShareQuotaLock(t *testing.T) {
	now := time.Now()
	rates := map[string]RateLimit{}
	var mu sync.Mutex
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	g := GitHub{Executable: filepath.Join(t.TempDir(), "gh"), now: func() time.Time { return now }, run: func(ctx context.Context, cmd *exec.Cmd) error {
		remaining, body := 2500, `{}`
		if strings.Contains(strings.Join(cmd.Args, " "), "actions/workflows") {
			remaining, body = 100, `{"workflows":[]}`
		}
		fmt.Fprintf(cmd.Stdout, "HTTP/2.0 200 OK\r\nX-RateLimit-Remaining: %d\r\nX-RateLimit-Reset: %d\r\n\r\n%s", remaining, now.Add(time.Hour).Unix(), body)
		arrived <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	g.requests = &requestCache{rates: rates, rateMu: &mu, used: map[string]cachedResponse{}, now: g.now}
	other := g
	other.requests = &requestCache{rates: rates, rateMu: &mu, used: map[string]cachedResponse{}, now: g.now}
	ref := Ref{Host: "github.com", Repo: "owner/workflow-lock"}
	done := make(chan error, 2)
	go func() { _, err := g.hasPRWorkflows(t.Context(), ref); done <- err }()
	go func() { _, _, err := other.request(t.Context(), ref, "repos/owner/workflow-lock/pulls/1"); done <- err }()
	<-arrived
	<-arrived
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	rate := rates[(RateLimitScope{Host: ref.Host, Resource: "core"}).Key()]
	if rate.Remaining == nil || *rate.Remaining != 100 {
		t.Fatalf("workflow quota was overwritten: %+v", rate)
	}
}

func TestArtifactRequestsUseSelectedAccountQuota(t *testing.T) {
	e := newEnv(t)
	accountGH(t, e)
	ref := Ref{Host: "github.com", Repo: "owner/repo", Number: 17}
	path := "repos/owner/repo/actions/runs/81/artifacts?per_page=100"
	reset := e.clock.Add(time.Hour)
	e.f.Raw = map[string]string{path: fmt.Sprintf("HTTP/2.0 200 OK\r\nX-RateLimit-Resource: core\r\nX-RateLimit-Remaining: 300\r\nX-RateLimit-Reset: %d\r\n\r\n{\"artifacts\":[]}", reset.Unix())}
	e.write()
	gh := artifactGitHub(e.s.github(e.author.RoomID))
	rates := map[string]RateLimit{}
	gh.requests = &requestCache{rates: rates, used: map[string]cachedResponse{}, now: e.s.now}
	if _, _, err := gh.request(t.Context(), ref, path); err != nil {
		t.Fatal(err)
	}
	scope := RateLimitScope{Host: ref.Host, Account: "fixture", Resource: "core"}
	rate := rates[scope.Key()]
	if len(rates) != 1 || rate.Remaining == nil || *rate.Remaining != 300 || !rate.Reset.Equal(reset) {
		t.Fatalf("artifact response lost selected account: %+v", rates)
	}
	rate.PauseUntil = reset
	rates[scope.Key()] = rate
	before, err := os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = gh.request(t.Context(), ref, path)
	var limited *rateLimitError
	if !errors.As(err, &limited) || !limited.until.Equal(reset) {
		t.Fatalf("artifact request bypassed account pause: %v", err)
	}
	after, err := os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("paused artifact request reached GitHub")
	}
	rate.PauseUntil = time.Time{}
	rates[scope.Key()] = rate
	e.f.Raw[path] = "HTTP/2.0 404 Not Found\r\n\r\n{}"
	e.write()
	if _, _, err := gh.request(t.Context(), ref, path); !permanentTestRequestError(err) {
		t.Fatalf("artifact HTTP error lost classification: %v", err)
	}
}

func TestArtifactInspectionAccountsForDownloadQuota(t *testing.T) {
	e := newEnv(t)
	accountGH(t, e)
	ref := Ref{Host: "github.com", Repo: "owner/repo", Number: 17}
	listing := "repos/owner/repo/actions/runs/81/artifacts?per_page=100"
	archive := "repos/owner/repo/actions/artifacts/91/zip"
	data := testReportZip(t, map[string]string{"results.xml": `<testsuite><testcase name="fast" time="1"/></testsuite>`})
	reset := e.clock.Add(time.Hour)
	e.f.Raw = map[string]string{
		listing: fmt.Sprintf("HTTP/2.0 200 OK\r\nX-RateLimit-Remaining: 500\r\nX-RateLimit-Reset: %d\r\n\r\n{\"artifacts\":[{\"id\":91,\"name\":\"test-report\",\"size_in_bytes\":%d}]}", reset.Unix(), len(data)),
	}
	e.f.Headers = map[string]string{"X-RateLimit-Remaining": "498", "X-RateLimit-Reset": fmt.Sprint(reset.Unix())}
	e.f.Blobs = map[string][]byte{archive: data}
	e.write()
	gh := e.s.github(e.author.RoomID)
	rates := map[string]RateLimit{}
	gh.requests = &requestCache{rates: rates, used: map[string]cachedResponse{}, now: e.s.now}
	report, found, problems, _, err := inspectTestRun(t.Context(), gh, Record{Host: ref.Host, Repo: ref.Repo, Number: ref.Number}, Run{ID: 81, Attempt: 1})
	if err != nil || !found || len(problems) != 0 || len(report.Tests) != 1 {
		t.Fatalf("inspection: found=%v problems=%v err=%v", found, problems, err)
	}
	scope := RateLimitScope{Host: ref.Host, Account: "fixture", Resource: "core"}
	rate := rates[scope.Key()]
	if len(rates) != 1 || rate.Remaining == nil || *rate.Remaining != 498 {
		t.Fatalf("download quota was not accounted: %+v", rates)
	}
}

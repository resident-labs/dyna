package prs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// quotaServer answers every request with the quota headers it is given, and keeps what it was sent.
type quotaServer struct {
	mu      sync.Mutex
	seen    []string
	bodies  []string
	headers map[string]http.Header // by path; a status other than 200 goes in "Status"
}

func (q *quotaServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	q.mu.Lock()
	q.seen = append(q.seen, r.Method+" "+r.URL.Path)
	q.bodies = append(q.bodies, string(data))
	header := q.headers[r.URL.Path]
	q.mu.Unlock()
	status := http.StatusOK
	for name, values := range header {
		if name == "Status" {
			status, _ = strconv.Atoi(values[0])
			continue
		}
		w.Header()[name] = values
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{}`))
}

func (q *quotaServer) set(path string, header http.Header) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.headers[path] = header
}

func (q *quotaServer) requests() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.seen...)
}

func readsClient(t *testing.T) (*Client, *quotaServer, *clock, *http.Client) {
	t.Helper()
	fake := &quotaServer{headers: map[string]http.Header{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	at := &clock{now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	c := &Client{Token: "engine-token", Endpoint: endpoint, Transport: http.DefaultTransport, Now: at.Now}
	return c, fake, at, &http.Client{Transport: c.Reads(http.DefaultTransport)}
}

func quotaHeader(resource string, limit, remaining int, reset time.Time) http.Header {
	return http.Header{"X-Ratelimit-Resource": {resource}, "X-Ratelimit-Limit": {strconv.Itoa(limit)}, "X-Ratelimit-Remaining": {strconv.Itoa(remaining)}, "X-Ratelimit-Reset": {strconv.FormatInt(reset.Unix(), 10)}}
}

func send(t *testing.T, client *http.Client, method, target, body string) error {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	return nil
}

// Agents read GitHub and nothing else: REST writes and GraphQL mutations never leave the engine, and a query arrives whole.
func TestAgentReadsRefuseWrites(t *testing.T) {
	c, fake, _, client := readsClient(t)
	base := c.Endpoint.String()
	for _, write := range []struct{ method, path, body string }{
		{http.MethodPost, "/repos/o/r/issues", `{"title":"x"}`},
		{http.MethodPatch, "/repos/o/r/pulls/1", `{}`},
		{http.MethodPut, "/repos/o/r/pulls/1/merge", `{}`},
		{http.MethodDelete, "/repos/o/r/git/refs/heads/main", ``},
		{http.MethodPost, "/graphql", `{"query":"mutation($id:ID!){closeIssue(input:{issueId:$id}){clientMutationId}}"}`},
		{http.MethodPost, "/graphql", `{"query":"query a{viewer{login}} mutation b{addStar(input:{starrableId:\"x\"}){clientMutationId}}"}`},
		{http.MethodPost, "/graphql", `not json`},
	} {
		err := send(t, client, write.method, base+write.path, write.body)
		if err == nil || !strings.Contains(err.Error(), "agents only read GitHub here") || !strings.Contains(err.Error(), "pr open") {
			t.Errorf("%s %s: %v", write.method, write.path, err)
		}
	}
	if seen := fake.requests(); len(seen) != 0 {
		t.Fatalf("writes reached GitHub: %v", seen)
	}
	query := `{"query":"query($o:String!){repository(owner:$o,name:\"r\"){name}}","variables":{"o":"o"}}`
	if err := send(t, client, http.MethodPost, base+"/graphql", query); err != nil {
		t.Fatal(err)
	}
	if err := send(t, client, http.MethodGet, base+"/repos/o/r/contents/README.md", ""); err != nil {
		t.Fatal(err)
	}
	if seen := fake.requests(); len(seen) != 2 || seen[0] != "POST /graphql" || fake.bodies[0] != query {
		t.Fatalf("reads: %v %q", seen, fake.bodies)
	}
}

// Agents' reads spend the feeds' quota: what GitHub says on them is the feeds' reading, and they stop while under 40% is left,
// before the feeds would slow at 20%.
func TestAgentReadsShareTheFeedsQuota(t *testing.T) {
	c, fake, at, client := readsClient(t)
	base := c.Endpoint.String()
	reset := at.Now().Add(30 * time.Minute)
	fake.set("/repos/o/r", quotaHeader("core", 5000, 2001, reset))
	if err := send(t, client, http.MethodGet, base+"/repos/o/r", ""); err != nil {
		t.Fatal(err)
	}
	if err := send(t, client, http.MethodGet, base+"/repos/o/r", ""); err != nil {
		t.Fatalf("41%% left: %v", err)
	}
	fake.set("/repos/o/r", quotaHeader("core", 5000, 1999, reset))
	if err := send(t, client, http.MethodGet, base+"/repos/o/r", ""); err != nil {
		t.Fatal(err)
	}
	err := send(t, client, http.MethodGet, base+"/repos/o/r/pulls/1/files", "")
	if err == nil || !strings.Contains(err.Error(), "wait until "+reset.UTC().Format(time.RFC3339)) || !strings.Contains(err.Error(), "1999 of 5000") || !strings.Contains(err.Error(), "PR and issue tracking") {
		t.Fatalf("under 40%%: %v", err)
	}
	if _, low := c.Low(); low {
		t.Fatal("the feeds slowed at 40%")
	}
	// GraphQL is a quota of its own, so agents still read through it.
	if err := send(t, client, http.MethodPost, base+"/graphql", `{"query":"{viewer{login}}"}`); err != nil {
		t.Fatalf("GraphQL: %v", err)
	}
	if got := len(fake.requests()); got != 4 {
		t.Fatalf("%d requests reached GitHub", got)
	}
	// An agent's read brings the feeds the news that the quota is low.
	at.add(31 * time.Minute)
	reset = at.Now().Add(time.Hour)
	fake.set("/repos/o/r", quotaHeader("core", 5000, 900, reset))
	if err := send(t, client, http.MethodGet, base+"/repos/o/r", ""); err != nil {
		t.Fatalf("after the reset: %v", err)
	}
	if until, low := c.Low(); !low || !until.Equal(reset) {
		t.Fatalf("the feeds did not hear of the low quota: %v %v", until, low)
	}
}

// Search has limits only agents spend: a refusal there holds back searches for as long as GitHub asks, and nothing else.
func TestAgentSearchWaitsOnlyForSearch(t *testing.T) {
	c, fake, at, client := readsClient(t)
	base := c.Endpoint.String()
	refused := quotaHeader("code_search", 10, 0, at.Now().Add(40*time.Second))
	refused.Set("Status", "403")
	fake.set("/search/code", refused)
	if err := send(t, client, http.MethodGet, base+"/search/code?q=x", ""); err != nil {
		t.Fatal(err)
	}
	err := send(t, client, http.MethodGet, base+"/search/code?q=y", "")
	if err == nil || !strings.Contains(err.Error(), "code search") || !strings.Contains(err.Error(), at.Now().Add(40*time.Second).UTC().Format(time.RFC3339)) {
		t.Fatalf("a second search: %v", err)
	}
	if err := send(t, client, http.MethodGet, base+"/search/issues?q=y", ""); err != nil {
		t.Fatalf("issue search has its own limit: %v", err)
	}
	if err := send(t, client, http.MethodGet, base+"/repos/o/r", ""); err != nil {
		t.Fatalf("a REST read: %v", err)
	}
	if _, low := c.Low(); low {
		t.Fatal("a search limit slowed the feeds")
	}
	at.add(41 * time.Second)
	if err := send(t, client, http.MethodGet, base+"/search/code?q=z", ""); err != nil {
		t.Fatalf("after the wait: %v", err)
	}
	// A secondary limit on the REST API stops the feeds too, as the token is the feeds'.
	limited := http.Header{"Retry-After": {"120"}, "Status": {"429"}}
	fake.set("/repos/o/r/commits", limited)
	if err := send(t, client, http.MethodGet, base+"/repos/o/r/commits", ""); err != nil {
		t.Fatal(err)
	}
	if until, low := c.Low(); !low || !until.Equal(at.Now().Add(2*time.Minute)) {
		t.Fatalf("a refusal on the shared quota: %v %v", until, low)
	}
	if err := send(t, client, http.MethodGet, base+"/repos/o/r", ""); err == nil || !strings.Contains(err.Error(), "GitHub asked to wait") {
		t.Fatalf("during the wait: %v", err)
	}
}

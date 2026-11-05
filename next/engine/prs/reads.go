package prs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// agentFloor is the share of the REST or GraphQL quota, in percent, that agents' reads leave to the engine. It is twice the 20%
// at which the feeds slow down (spec 7.7), so agents can never push the feeds into their slow pace: PR and issue wakes keep theirs.
const agentFloor = 40

// agentSlots bounds agents' requests in flight across every box, far inside GitHub's secondary limit of 100.
const agentSlots = 8

// writing finds a GraphQL operation that is not a read.
var writing = regexp.MustCompile(`\b(mutation|subscription)\b`)

// Reads returns the transport for agents' GitHub reads (spec 7.7 rule 5). It sends only reads: REST GET and HEAD, and GraphQL
// queries. It spends the quota the feeds spend, so what GitHub says of a quota on an agent's read is the feeds' reading too, and
// it stops agents while less than agentFloor of that quota is left, or while GitHub has asked for a wait. Search has quotas of
// its own, which only agents spend, so a search waits only for GitHub. A host other than the REST API's, such as raw file
// content, has no quota here.
func (c *Client) Reads(base http.RoundTripper) http.RoundTripper {
	return &reads{client: c, base: base, slots: make(chan struct{}, agentSlots)}
}

type reads struct {
	client *Client
	base   http.RoundTripper
	slots  chan struct{}
}

func (r *reads) RoundTrip(request *http.Request) (*http.Response, error) {
	resource := r.client.resource(request)
	request, err := readOnly(request, resource)
	if err != nil {
		return nil, err
	}
	if err := r.client.agentWait(resource); err != nil {
		if request.Body != nil {
			request.Body.Close()
		}
		return nil, err
	}
	select {
	case r.slots <- struct{}{}:
	case <-request.Context().Done():
		if request.Body != nil {
			request.Body.Close()
		}
		return nil, request.Context().Err()
	}
	defer func() { <-r.slots }()
	response, err := r.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	r.client.agentNote(resource, response)
	return response, nil
}

// resource names the quota a request spends, as GitHub's X-RateLimit-Resource does, or "" for none here.
func (c *Client) resource(request *http.Request) string {
	if !strings.EqualFold(request.URL.Host, c.Endpoint.Host) {
		return ""
	}
	path := strings.TrimPrefix(request.URL.Path, strings.TrimRight(c.Endpoint.Path, "/"))
	switch {
	case path == "/graphql":
		return "graphql"
	case path == "/search/code":
		return "code_search"
	case strings.HasPrefix(path, "/search/"):
		return "search"
	}
	return "core"
}

// Vouched marks a request context as one the engine has itself checked as an allowed write, which Reads then lets out. Only code
// in the engine process can set it: a box sends requests only to the MCP server, which builds its own.
func Vouched(ctx context.Context) context.Context { return context.WithValue(ctx, vouchedKey{}, true) }

type vouchedKey struct{}

// readOnly refuses anything but a read. A GraphQL query is a POST, so its document is read here and put back.
func readOnly(request *http.Request, resource string) (*http.Request, error) {
	refuse := func() (*http.Request, error) {
		if request.Body != nil {
			request.Body.Close()
		}
		return nil, fmt.Errorf("agents only read GitHub here, and %s %s would write: open a PR with pr open, and leave other writes to the owner or a host", request.Method, request.URL.Path)
	}
	switch {
	case request.Method == http.MethodGet || request.Method == http.MethodHead || request.Context().Value(vouchedKey{}) == true:
		return request, nil
	case request.Method != http.MethodPost || resource != "graphql" || request.Body == nil:
		return refuse()
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	request.Body.Close()
	if err != nil {
		return nil, err
	}
	var document struct {
		Query string `json:"query"`
	}
	if json.Unmarshal(data, &document) != nil || document.Query == "" || writing.MatchString(document.Query) {
		request.Body = nil
		return refuse()
	}
	request = request.Clone(request.Context())
	request.Body, request.ContentLength = io.NopCloser(bytes.NewReader(data)), int64(len(data))
	request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	return request, nil
}

// quotaOf is the quota a resource spends. Call it with c.mu held.
func (c *Client) quotaOf(resource string) *quota {
	switch resource {
	case "core":
		return &c.rest
	case "graphql":
		return &c.graphql
	}
	if c.others == nil {
		c.others = map[string]*quota{}
	}
	if c.others[resource] == nil {
		c.others[resource] = &quota{}
	}
	return c.others[resource]
}

// shared reports whether the feeds spend a resource too.
func shared(resource string) bool { return resource == "core" || resource == "graphql" }

// agentWait refuses a read that the quota cannot serve now, naming when it can.
func (c *Client) agentWait(resource string) error {
	if resource == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now, q := c.Now(), c.quotaOf(resource)
	name := map[string]string{"core": "REST API", "graphql": "GraphQL"}[resource]
	if name == "" {
		name = strings.ReplaceAll(resource, "_", " ")
	}
	if now.Before(q.until) {
		return fmt.Errorf("GitHub asked to wait before more %s requests: try again after %s", name, q.until.UTC().Format(time.RFC3339))
	}
	if !now.Before(q.reset) || q.limit <= 0 {
		return nil
	}
	if shared(resource) && q.remaining*100 < q.limit*agentFloor {
		return fmt.Errorf("GitHub reads for agents wait until %s: %d of %d %s requests are left this hour, and the engine keeps the last %d%% for PR and issue tracking", q.reset.UTC().Format(time.RFC3339), q.remaining, q.limit, name, agentFloor)
	}
	if !shared(resource) && q.remaining == 0 {
		return fmt.Errorf("GitHub's %s limit is used up: try again after %s", name, q.reset.UTC().Format(time.RFC3339))
	}
	return nil
}

// agentNote records what GitHub said of the quota a read spent, and a wait it asked for.
func (c *Client) agentNote(resource string, response *http.Response) {
	if named := response.Header.Get("X-RateLimit-Resource"); named != "" && resource != "" {
		resource = named
	}
	if resource == "" {
		return
	}
	header := response.Header
	limit, limitErr := strconv.Atoi(header.Get("X-RateLimit-Limit"))
	remaining, remainingErr := strconv.Atoi(header.Get("X-RateLimit-Remaining"))
	reset, resetErr := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64)
	c.mu.Lock()
	q := c.quotaOf(resource)
	if limitErr == nil && remainingErr == nil && resetErr == nil {
		q.note(limit, remaining, time.Unix(reset, 0))
	}
	c.mu.Unlock()
	if response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusTooManyRequests || header.Get("Retry-After") == "" && header.Get("X-RateLimit-Remaining") != "0" {
		return
	}
	if shared(resource) {
		c.refused(q, header)
		return
	}
	// Search limits last a minute, so a refusal there waits only as long as GitHub says.
	until := c.Now().Add(time.Minute)
	if seconds, err := strconv.Atoi(header.Get("Retry-After")); err == nil {
		until = c.Now().Add(time.Duration(seconds) * time.Second)
	} else if resetErr == nil {
		until = time.Unix(reset, 0)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if until.After(q.until) {
		q.until = until
	}
}

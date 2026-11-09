package githubwrites

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs"
)

// Guard is the transport between GitHub's MCP server and GitHub. A read goes through to Base, which is the engine's read
// transport (spec 7.7 rule 5). A write goes through only when the allow-list of the room on the request's context permits it;
// then it is vouched for, which is the only way Base lets a write out. Redirects arrive here as new requests and are checked
// again.
type Guard struct {
	// CanPush checks the existing push rule with the trusted caller on the context.
	CanPush func(context.Context, contract.RepoName, string) (bool, error)
	// Owns checks the recorded PR owner against the trusted session on the context.
	Owns func(context.Context, contract.RepoName, int) (bool, error)
	Base http.RoundTripper
	API  *url.URL // GitHub's REST API; its path is the prefix of every REST route, and its host the only one writes go to
}

func (g *Guard) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return g.Base.RoundTrip(request)
	}
	if request.Body != nil {
		defer func() {
			if request.Body != nil {
				request.Body.Close()
			}
		}()
	}
	grant := AllowedFrom(request.Context())
	prefix := strings.TrimRight(g.API.Path, "/")
	// A GraphQL query is a read, which the engine always allows; whether a POST to /graphql is one is known only once its body is
	// parsed, so that is checked before the setting is.
	graphQL := request.Method == http.MethodPost && request.URL.Path == prefix+"/graphql" && request.URL.RawQuery == ""
	if !grant.On() && !graphQL {
		return nil, refuse(contract.GitHubWriteOff, "agents only read GitHub here, and %s %s would write: this room's githubWrites setting is off, so open a PR with pr open, and leave other writes to the owner or a host", request.Method, request.URL.Path)
	}
	if !strings.EqualFold(request.URL.Host, g.API.Host) || request.URL.Scheme != g.API.Scheme || request.URL.User != nil {
		return nil, refuse(contract.GitHubWriteRouteNotAllowed, "%s %s is not on GitHub's API host", request.Method, request.URL.Path)
	}
	// Upstream sends bodyless POSTs for these run operations, without Content-Type.
	segments, canonical := segmentsOf(request, prefix)
	bodylessRun := canonical && request.Method == http.MethodPost && len(segments) == 7 &&
		segments[0] == "repos" && segments[3] == "actions" && segments[4] == "runs" && numberPart.MatchString(segments[5]) &&
		(segments[6] == "rerun" || segments[6] == "rerun-failed-jobs" || segments[6] == "cancel") &&
		(request.Body == nil || request.Body == http.NoBody)
	if kind, _, err := mime.ParseMediaType(request.Header.Get("Content-Type")); !bodylessRun && (err != nil || kind != "application/json") {
		return nil, refuse(contract.GitHubWriteContentType, "%s %s has the content type %q; GitHub writes are sent as application/json", request.Method, request.URL.Path, request.Header.Get("Content-Type"))
	}
	body := []byte("{}")
	if !bodylessRun {
		var refusal *Refusal
		body, request, refusal = readBody(request)
		if refusal != nil {
			return nil, refusal
		}
	}
	if graphQL {
		mutates, refusal := g.checkGraphQL(request, grant, body)
		if refusal != nil {
			return nil, refusal
		}
		if !mutates {
			// A query goes on as the read it is, with no vouch, so the read transport still checks it.
			return g.Base.RoundTrip(request)
		}
	} else if refusal := g.checkREST(grant, request, prefix, body); refusal != nil {
		return nil, refusal
	}
	return g.Base.RoundTrip(request.WithContext(prs.Vouched(request.Context())))
}

// ask sends a read the guard needs, with the credentials of the request it checks, and decodes the JSON answer. A failed lookup
// keeps its cause: the read transport can refuse locally, and GitHub can return a non-200 status or an unreadable answer.
func (g *Guard) ask(request *http.Request, method, target string, body []byte, into any) error {
	asked, err := http.NewRequestWithContext(request.Context(), method, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("GitHub lookup request could not be built: %w", err)
	}
	for _, name := range []string{"Authorization", "User-Agent"} {
		asked.Header.Set(name, request.Header.Get(name))
	}
	if body != nil {
		asked.Header.Set("Content-Type", "application/json")
	}
	response, err := g.Base.RoundTrip(asked)
	if err != nil {
		return fmt.Errorf("GitHub lookup transport failed or refused the request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub lookup returned HTTP %d %s", response.StatusCode, http.StatusText(response.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("GitHub lookup response could not be read: %w", err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("GitHub lookup returned invalid JSON: %w", err)
	}
	return nil
}

// isPullRequest says whether an issue number of a repository is a PR, which the issues API reaches by the same number. A lookup
// that fails counts as yes, so an unclear case is refused.
func (g *Guard) isPullRequest(request *http.Request, at matched) bool {
	var issue struct {
		PullRequest *json.RawMessage `json:"pull_request"`
	}
	target := *g.API
	target.Path = strings.TrimRight(g.API.Path, "/") + "/repos/" + at.owner + "/" + at.repo + "/issues/" + at.number
	return g.ask(request, http.MethodGet, target.String(), nil, &issue) != nil || issue.PullRequest != nil
}

// node is what the guard learns of a GraphQL node ID before it lets a mutation name it.
type node struct {
	Number     int    `json:"number"`
	Typename   string `json:"__typename"`
	Name       string `json:"nameWithOwner"`
	Repository *struct {
		Name string `json:"nameWithOwner"`
	} `json:"repository"`
	Owner   *struct{ Login string } `json:"owner"`
	Project *struct {
		Owner *struct{ Login string } `json:"owner"`
	} `json:"project"`
	Content *struct {
		Typename   string `json:"__typename"`
		Repository *struct {
			Name string `json:"nameWithOwner"`
		} `json:"repository"`
	} `json:"content"`
}

const nodeQuery = `query($id:ID!){node(id:$id){__typename
 ... on Repository{nameWithOwner}
 ... on Issue{repository{nameWithOwner}}
 ... on PullRequest{number repository{nameWithOwner}}
 ... on Label{repository{nameWithOwner}}
 ... on Milestone{repository{nameWithOwner}}
 ... on PullRequestReview{repository{nameWithOwner}}
 ... on PullRequestReviewThread{repository{nameWithOwner}}
 ... on ProjectV2{owner{... on User{login} ... on Organization{login}}}
 ... on ProjectV2Item{project{owner{... on User{login} ... on Organization{login}}} content{__typename ... on Issue{repository{nameWithOwner}} ... on PullRequest{repository{nameWithOwner}}}}}}`

// within looks a node ID up under the engine's own token and requires it to be what the mutation expects and to belong to the
// allowed set: a repository's nodes by repository, a project by its owner, and a project item by both its project's owner and its
// content's repository. A lookup the guard cannot make or read is a refusal.
func (g *Guard) within(request *http.Request, grant contract.GitHubWrites, seen map[string]node, id string, kind nodeKind, mutationName string) *Refusal {
	found, ok := seen[id]
	if !ok {
		var answer struct {
			Data struct {
				Node *node `json:"node"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		query, _ := json.Marshal(map[string]any{"query": nodeQuery, "variables": map[string]string{"id": id}})
		target := *request.URL
		if err := g.ask(request, http.MethodPost, target.String(), query, &answer); err != nil {
			return refuse(contract.GitHubWriteGraphQLUnresolved, "the node ID given to %s did not resolve to an object of a repository the engine could look up: %s", mutationName, err)
		}
		if answer.Data.Node == nil {
			detail := "GitHub returned no node"
			if len(answer.Errors) != 0 {
				detail = fmt.Sprintf("GitHub returned GraphQL error: %.512s", answer.Errors[0].Message)
			}
			return refuse(contract.GitHubWriteGraphQLUnresolved, "the node ID given to %s did not resolve to an object of a repository the engine could look up: %s", mutationName, detail)
		}
		found = *answer.Data.Node
		seen[id] = found
	}
	inRepo := func(repo *struct {
		Name string `json:"nameWithOwner"`
	}) bool {
		return repo != nil && grant.Allows(repo.Name)
	}
	var allowed bool
	switch kind {
	case nodeRepository:
		allowed = found.Typename == string(kind) && grant.Allows(found.Name)
	case nodeIssue, nodePullRequest, nodeReview, nodeThread, nodeLabel, nodeMilestone:
		allowed = found.Typename == string(kind) && inRepo(found.Repository)
	case nodeContent:
		allowed = (found.Typename == string(nodeIssue) || found.Typename == string(nodePullRequest)) && inRepo(found.Repository)
	case nodeProject:
		allowed = found.Typename == string(kind) && found.Owner != nil && grant.AllowsOwner(found.Owner.Login)
	case nodeItem:
		allowed = found.Typename == string(kind) && found.Project != nil && found.Project.Owner != nil && grant.AllowsOwner(found.Project.Owner.Login) && found.Content != nil && inRepo(found.Content.Repository)
	}
	if allowed {
		return nil
	}
	return refuse(contract.GitHubWriteRepoNotAllowed, "the node ID given to %s is not a %s of a repository (or a project of an owner) this room allows", mutationName, kind)
}

// requireOwner fails closed when the ownership lookup is unavailable, preserving store errors.
func (g *Guard) requireOwner(ctx context.Context, repo contract.RepoName, number int) error {
	if g.Owns != nil {
		owns, err := g.Owns(ctx, repo, number)
		if err != nil {
			return fmt.Errorf("read PR ownership: %w", err)
		}
		if owns {
			return nil
		}
	}
	return refuse(contract.GitHubWritePRNotOwned, "only the session that owns this PR, or a session above it, can close, reopen or retarget it")
}

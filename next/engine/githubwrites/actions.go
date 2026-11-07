package githubwrites

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

var commitRef = regexp.MustCompile(`(?i)^[0-9a-f]{40}([0-9a-f]{24})?$`)

func (g *Guard) checkActions(request *http.Request, found matched, segments []string, prefix string, body []byte) error {
	repo := contract.RepoName(found.owner + "/" + found.repo)
	if SessionFrom(request.Context()) == "" || g.CanPush == nil {
		return refuse(contract.GitHubWriteBranchNotAllowed, "Actions writes need a trusted caller and branch authorization")
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(body, &keys) != nil || keys == nil {
		return refuse(contract.GitHubWriteBodyUnreadable, "Actions writes need a JSON object")
	}
	var branch string
	if segments[4] == "workflows" {
		if !keysWithin(keys, []string{"ref", "inputs"}) || json.Unmarshal(keys["ref"], &branch) != nil {
			return refuse(contract.GitHubWriteFieldNotAllowed, "workflow dispatch accepts only ref and inputs, with a branch ref")
		}
		branch = strings.TrimPrefix(branch, "refs/heads/")
	} else {
		// Run IDs identify immutable runs, not a mutable branch-to-run alias. Check the run just before forwarding.
		target := *g.API
		target.Path = prefix + "/repos/" + string(repo) + "/actions/runs/" + found.number
		var run struct {
			HeadBranch     string `json:"head_branch"`
			HeadRepository struct {
				FullName string `json:"full_name"`
			} `json:"head_repository"`
		}
		if err := g.ask(request, http.MethodGet, target.String(), nil, &run); err != nil {
			return refuse(contract.GitHubWriteIDUnresolved, "Actions run lookup failed: %v", err)
		}
		if !strings.EqualFold(run.HeadRepository.FullName, string(repo)) {
			return refuse(contract.GitHubWriteRepoNotAllowed, "Actions runs from forks or unknown repositories are refused")
		}
		branch = run.HeadBranch
	}
	if branch == "" || strings.HasPrefix(branch, "refs/") || commitRef.MatchString(branch) {
		return refuse(contract.GitHubWriteBranchNotAllowed, "Actions writes require a branch, not a tag or commit SHA")
	}
	allowed, err := g.CanPush(request.Context(), repo, branch)
	if err != nil || !allowed {
		return refuse(contract.GitHubWriteBranchNotAllowed, "Actions branch %q is not pushable by this session (lookup error: %v)", branch, err)
	}
	if segments[4] == "workflows" {
		// Pin the forwarded ref to heads: a bare name can also name a tag on GitHub.
		keys["ref"], _ = json.Marshal("refs/heads/" + branch)
		normalized, err := json.Marshal(keys)
		if err != nil {
			return refuse(contract.GitHubWriteBodyUnreadable, "workflow dispatch body could not be encoded: %v", err)
		}
		request.Body = io.NopCloser(bytes.NewReader(normalized))
		request.ContentLength = int64(len(normalized))
		request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(normalized)), nil }
	}
	return nil
}

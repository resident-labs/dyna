package githubwrites

import (
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// Tool is one of upstream's write tools (github-mcp-server v2.0.2) with the groups that register it and, for each, one sentence
// that says what the room cannot do with it. The transport, not the tool, is what refuses.
type Tool struct {
	Name  string
	Notes map[contract.GitHubWriteGroup]string
}

const (
	issues   = contract.GitHubWriteIssues
	pulls    = contract.GitHubWritePulls
	projects = contract.GitHubWriteProjects
)

// Tools lists the write tools a room's groups can register. Every other upstream write tool stays unregistered.
var Tools = []Tool{
	{"actions_run_trigger", map[contract.GitHubWriteGroup]string{contract.GitHubWriteActions: "Runs, re-runs and cancels Actions runs on branches this session may push (its own branch, its `-topic` branches, and branches of PRs it owns). Deleting logs, approving runs and runs from forks are refused."}},
	{"issue_write", map[contract.GitHubWriteGroup]string{
		issues: "It creates and edits issues of the allowed repositories; a label name that does not exist yet is created as a new label; clearing an issue field value sends a DELETE, which is never allowed.",
		pulls:  "With only pulls on it sets the labels of an issue or PR and nothing else (a label name that does not exist yet is created as a new label): creating an issue and every other field are refused.",
	}},
	{"add_issue_comment", map[contract.GitHubWriteGroup]string{
		issues: "It comments on issues of the allowed repositories.",
		pulls:  "It comments on PRs of the allowed repositories.",
	}},
	{"update_issue_comment", map[contract.GitHubWriteGroup]string{
		issues: "It edits comments of issues of the allowed repositories.",
		pulls:  "It edits comments of PRs of the allowed repositories.",
	}},
	{"label_write", map[contract.GitHubWriteGroup]string{
		issues: "It creates and edits labels of the allowed repositories ('create' and 'update'); 'delete' is never allowed.",
	}},
	{"update_pull_request", map[contract.GitHubWriteGroup]string{
		pulls: "It edits a PR's title, body and draft flag; the session that owns the PR may also close, reopen or change its base. Merges, reviewers and maintainer_can_modify are refused.",
	}},
	{"add_reply_to_pull_request_comment", map[contract.GitHubWriteGroup]string{
		pulls: "It replies to review comments of PRs of the allowed repositories.",
	}},
	{"pull_request_review_write", map[contract.GitHubWriteGroup]string{
		pulls: "'create', 'submit_pending', 'resolve_thread' and 'unresolve_thread' work. A review is a comment or a change request: the event APPROVE is refused, and 'submit_pending' needs the event COMMENT or REQUEST_CHANGES. 'delete_pending' and dismissing a review are never allowed.",
	}},
	{"projects_write", map[contract.GitHubWriteGroup]string{
		projects: "'add_project_item', 'update_project_item' and 'update_project_items' work on projects owned by the owner of an allowed repository, and an added item must be an issue or PR of an allowed repository. Creating or deleting projects, items, views, status updates and iteration fields is never allowed.",
	}},
}

// StrippedParams are the arguments a tool's listed schema leaves out, because the transport refuses them.
var StrippedParams = map[string][]string{
	"update_pull_request": {"reviewers", "maintainer_can_modify"},
}

// ToolNames lists the write tools a grant's groups register.
func ToolNames(grant contract.GitHubWrites) []string {
	var names []string
	for _, tool := range Tools {
		for group := range tool.Notes {
			if grant.Has(group) {
				names = append(names, tool.Name)
				break
			}
		}
	}
	slices.Sort(names)
	return names
}

// Note is what a grant cannot do with a tool, in the order of the groups, or "" for a tool the grant does not register.
func Note(grant contract.GitHubWrites, name string) string {
	var notes []string
	for _, tool := range Tools {
		if tool.Name != name {
			continue
		}
		for _, group := range contract.AllGitHubWriteGroups {
			if note, ok := tool.Notes[group]; ok && grant.Has(group) {
				notes = append(notes, note)
			}
		}
	}
	return strings.Join(notes, " ")
}

// Describe adds to a write tool's listed description what the grant cannot do with it, and takes the arguments the transport
// refuses out of its listed schema. The transport refuses them either way.
func Describe(grant contract.GitHubWrites, tool *mcp.Tool) {
	if note := Note(grant, tool.Name); note != "" {
		tool.Description += " " + note
	}
	if schema, ok := tool.InputSchema.(*jsonschema.Schema); ok {
		for _, param := range StrippedParams[tool.Name] {
			delete(schema.Properties, param)
		}
	}
}

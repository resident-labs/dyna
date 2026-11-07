package githubwrites

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// nodeKind says what a node ID in a mutation's input must be, and so how its repository is found.
type nodeKind string

const (
	nodeRepository  nodeKind = "Repository"
	nodeIssue       nodeKind = "Issue"
	nodePullRequest nodeKind = "PullRequest"
	nodeReview      nodeKind = "PullRequestReview"
	nodeThread      nodeKind = "PullRequestReviewThread"
	nodeLabel       nodeKind = "Label"
	nodeMilestone   nodeKind = "Milestone"
	nodeProject     nodeKind = "ProjectV2"
	nodeItem        nodeKind = "ProjectV2Item"
	nodeContent     nodeKind = "IssueOrPullRequest" // an Issue or a PullRequest
)

// ref is an input field that holds a node ID, with what that node must be. A required ref that is missing refuses the mutation,
// since the engine could not tell which repository it acts on.
type ref struct {
	field    string
	kind     nodeKind
	required bool
}

// enum is an input field that holds an enum value, which must be one of allowed (compared exactly, as GraphQL enums are). A required one
// that is missing refuses the mutation.
type enum struct {
	field    string
	allowed  []string
	required bool
}

// reviewEvents are the events a review may have: a comment or a change request. APPROVE can satisfy a required-review branch
// protection under the engine's token, which is a way to merge, and merges are never allowed.
var reviewEvents = []string{"COMMENT", "REQUEST_CHANGES"}

// mutation is one GraphQL mutation a group allows: the exact input fields it may carry, and the node IDs among them.
type mutation struct {
	group  contract.GitHubWriteGroup
	fields []string
	refs   []ref
	lists  []ref // input fields that hold a list of node IDs, each of which must be what kind says; a missing one is fine
	enums  []enum
}

// mutations is the allow-list of GraphQL mutations by root field name. A mutation not here is refused, whatever it is.
var mutations = map[string]mutation{
	"closePullRequest":  {group: pulls, fields: []string{"pullRequestId", "clientMutationId"}, refs: []ref{{"pullRequestId", nodePullRequest, true}}},
	"reopenPullRequest": {group: pulls, fields: []string{"pullRequestId", "clientMutationId"}, refs: []ref{{"pullRequestId", nodePullRequest, true}}},
	"createIssue": {group: issues, fields: []string{"repositoryId", "title", "body", "assigneeIds", "milestoneId", "labelIds", "parentIssueId", "clientMutationId"},
		refs:  []ref{{"repositoryId", nodeRepository, true}, {"parentIssueId", nodeIssue, false}, {"milestoneId", nodeMilestone, false}},
		lists: []ref{{"labelIds", nodeLabel, false}}},
	"closeIssue":  {group: issues, fields: []string{"issueId", "stateReason", "duplicateIssueId", "clientMutationId"}, refs: []ref{{"issueId", nodeIssue, true}, {"duplicateIssueId", nodeIssue, false}}},
	"reopenIssue": {group: issues, fields: []string{"issueId", "clientMutationId"}, refs: []ref{{"issueId", nodeIssue, true}}},
	"createLabel": {group: issues, fields: []string{"repositoryId", "name", "color", "description", "clientMutationId"}, refs: []ref{{"repositoryId", nodeRepository, true}}},
	"updateLabel": {group: issues, fields: []string{"id", "name", "color", "description", "clientMutationId"}, refs: []ref{{"id", nodeLabel, true}}},

	"updatePullRequest":             {group: pulls, fields: []string{"pullRequestId", "title", "body", "baseRefName", "clientMutationId"}, refs: []ref{{"pullRequestId", nodePullRequest, true}}},
	"markPullRequestReadyForReview": {group: pulls, fields: []string{"pullRequestId", "clientMutationId"}, refs: []ref{{"pullRequestId", nodePullRequest, true}}},
	"convertPullRequestToDraft":     {group: pulls, fields: []string{"pullRequestId", "clientMutationId"}, refs: []ref{{"pullRequestId", nodePullRequest, true}}},
	"addPullRequestReview":          {group: pulls, fields: []string{"pullRequestId", "commitOID", "body", "event", "clientMutationId"}, refs: []ref{{"pullRequestId", nodePullRequest, true}}, enums: []enum{{"event", reviewEvents, false}}},
	"submitPullRequestReview":       {group: pulls, fields: []string{"pullRequestId", "pullRequestReviewId", "event", "body", "clientMutationId"}, refs: []ref{{"pullRequestReviewId", nodeReview, true}, {"pullRequestId", nodePullRequest, false}}, enums: []enum{{"event", reviewEvents, true}}},
	"resolveReviewThread":           {group: pulls, fields: []string{"threadId", "clientMutationId"}, refs: []ref{{"threadId", nodeThread, true}}},
	"unresolveReviewThread":         {group: pulls, fields: []string{"threadId", "clientMutationId"}, refs: []ref{{"threadId", nodeThread, true}}},
	"addProjectV2ItemById":          {group: projects, fields: []string{"projectId", "contentId", "clientMutationId"}, refs: []ref{{"projectId", nodeProject, true}, {"contentId", nodeContent, true}}},
	"updateProjectV2ItemFieldValue": {group: projects, fields: []string{"projectId", "itemId", "fieldId", "value", "clientMutationId"}, refs: []ref{{"projectId", nodeProject, true}, {"itemId", nodeItem, true}}},
}

// request is a GraphQL request body as GitHub reads it.
type graphqlBody struct {
	Query     string
	Variables map[string]json.RawMessage
}

// readGraphQL reads a body as one GraphQL request with query text, and refuses a batch, a persisted query or extensions and any
// key GitHub's endpoint does not take.
func readGraphQL(body []byte) (graphqlBody, *Refusal) {
	var parsed graphqlBody
	if first := bytes.TrimSpace(body); len(first) > 0 && first[0] == '[' {
		return parsed, refuse(contract.GitHubWriteGraphQLBatch, "a GraphQL request is one operation document, not an array of them")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &keys); err != nil {
		return parsed, refuse(contract.GitHubWriteBodyUnreadable, "the GraphQL body is not one JSON object")
	}
	for key := range keys {
		switch key {
		case "query", "variables", "operationName":
		case "extensions":
			return parsed, refuse(contract.GitHubWriteGraphQLPersisted, "GraphQL extensions, which carry persisted queries, are not sent")
		default:
			return parsed, refuse(contract.GitHubWriteFieldNotAllowed, "the GraphQL body has a key %q that GitHub's endpoint does not take", key)
		}
	}
	if err := json.Unmarshal(keys["query"], &parsed.Query); err != nil || strings.TrimSpace(parsed.Query) == "" {
		return parsed, refuse(contract.GitHubWriteGraphQLPersisted, "the GraphQL body has no query text, as a persisted query has none")
	}
	if raw, ok := keys["variables"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &parsed.Variables); err != nil {
			return parsed, refuse(contract.GitHubWriteBodyUnreadable, "the GraphQL variables are not a JSON object")
		}
	}
	return parsed, nil
}

// checkGraphQL allows a GraphQL request whose every operation is a query, or a mutation that the grant allows. Every operation of
// the document counts, whichever one operationName selects.
func (g *Guard) checkGraphQL(request *http.Request, grant contract.GitHubWrites, body []byte) (mutates bool, err error) {
	parsed, refusal := readGraphQL(body)
	if refusal != nil {
		return false, refusal
	}
	document, parseErr := parser.ParseQuery(&ast.Source{Name: "request", Input: parsed.Query})
	if parseErr != nil {
		return false, refuse(contract.GitHubWriteGraphQLUnparsable, "the GraphQL document does not parse: %s", parseErr)
	}
	var writes []*ast.OperationDefinition
	for _, operation := range document.Operations {
		switch operation.Operation {
		case ast.Subscription:
			return false, refuse(contract.GitHubWriteGraphQLSubscription, "a GraphQL subscription is not sent")
		case ast.Mutation:
			writes = append(writes, operation)
		}
	}
	if len(writes) == 0 {
		return false, nil
	}
	if !grant.On() {
		return true, refuse(contract.GitHubWriteOff, "agents only read GitHub here, and this GraphQL request would write: the room's githubWrites setting is off, so open a PR with pr open, and leave other writes to the owner or a host")
	}
	nodes := map[string]node{}
	for _, operation := range writes {
		for _, selection := range operation.SelectionSet {
			field, ok := selection.(*ast.Field)
			if !ok || len(field.Directives) != 0 {
				return false, refuse(contract.GitHubWriteGraphQLShape, "a mutation's root is plain fields: no fragment and no directive")
			}
			rule, known := mutations[field.Name]
			if !known {
				return false, refuse(contract.GitHubWriteGraphQLMutation, "the GraphQL mutation %s is not among the GitHub writes a room can allow", field.Name)
			}
			if !grant.Has(rule.group) {
				return false, refuse(contract.GitHubWriteGroupOff, "the GraphQL mutation %s belongs to the %s group, which this room has not turned on", field.Name, rule.group)
			}
			if len(field.Arguments) != 1 || field.Arguments[0].Name != "input" || field.Arguments[0].Value.Kind != ast.Variable {
				return false, refuse(contract.GitHubWriteGraphQLShape, "the GraphQL mutation %s is sent as %s(input: $variable) and nothing else", field.Name, field.Name)
			}
			raw, given := parsed.Variables[field.Arguments[0].Value.Raw]
			var input map[string]json.RawMessage
			if !given || json.Unmarshal(raw, &input) != nil || input == nil {
				return false, refuse(contract.GitHubWriteGraphQLShape, "the input of %s is a variable whose value the request gives as an object", field.Name)
			}
			if !keysWithin(input, rule.fields) {
				return false, refuse(contract.GitHubWriteFieldNotAllowed, "the input of %s sets a field outside the ones this room allows (%s)", field.Name, strings.Join(rule.fields, ", "))
			}
			for _, each := range rule.enums {
				value, present := input[each.field]
				if !present && !each.required {
					continue
				}
				var given string
				if !present || json.Unmarshal(value, &given) != nil || !slices.Contains(each.allowed, given) {
					return false, refuse(contract.GitHubWriteReviewEvent, "the %s of %s is one of %s: a review is a comment or a change request, and approving is not available", each.field, field.Name, strings.Join(each.allowed, ", "))
				}
			}
			for _, each := range rule.refs {
				value, present := input[each.field]
				if !present {
					if each.required {
						return false, refuse(contract.GitHubWriteGraphQLShape, "the input of %s has no %s, which says which repository it acts on", field.Name, each.field)
					}
					continue
				}
				var id string
				if json.Unmarshal(value, &id) != nil || id == "" {
					return false, refuse(contract.GitHubWriteGraphQLShape, "%s of %s is a node ID string", each.field, field.Name)
				}
				if refusal := g.within(request, grant, nodes, id, each.kind, field.Name); refusal != nil {
					return false, refusal
				}
				_, retarget := input["baseRefName"]
				if field.Name == "closePullRequest" || field.Name == "reopenPullRequest" || (field.Name == "updatePullRequest" && retarget) {
					found := nodes[id]
					if found.Number <= 0 {
						return false, refuse(contract.GitHubWriteGraphQLUnresolved, "GitHub returned no PR number")
					}
					if err := g.requireOwner(request.Context(), contract.RepoName(strings.ToLower(found.Repository.Name)), found.Number); err != nil {
						return false, err
					}
				}
			}
			for _, each := range rule.lists {
				value, present := input[each.field]
				if !present {
					continue
				}
				var ids []string
				if json.Unmarshal(value, &ids) != nil || slices.Contains(ids, "") {
					return false, refuse(contract.GitHubWriteGraphQLShape, "%s of %s is a list of node ID strings", each.field, field.Name)
				}
				for _, id := range ids {
					if refusal := g.within(request, grant, nodes, id, each.kind, field.Name); refusal != nil {
						return false, refusal
					}
				}
			}
		}
	}
	return true, nil
}

// readBody reads a request body up to a megabyte, which is far over any write, and puts it back. A body of another content type
// is refused: JSON is what the routes take.
func readBody(request *http.Request) ([]byte, *http.Request, *Refusal) {
	if request.Body == nil {
		return nil, request, refuse(contract.GitHubWriteBodyUnreadable, "%s %s has no body", request.Method, request.URL.Path)
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, 1<<20+1))
	request.Body.Close()
	if err != nil || len(data) > 1<<20 {
		return nil, request, refuse(contract.GitHubWriteBodyUnreadable, "the body of %s %s could not be read whole, or is over 1 MiB", request.Method, request.URL.Path)
	}
	request = request.Clone(request.Context())
	request.Body, request.ContentLength = io.NopCloser(bytes.NewReader(data)), int64(len(data))
	request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	return data, request, nil
}

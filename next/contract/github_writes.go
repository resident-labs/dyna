package contract

import (
	"slices"
	"strings"
)

// GitHubWriteGroup names a set of GitHub writes a room's boxes may make on the room's repositories (spec 7.9).
type GitHubWriteGroup string

const (
	GitHubWriteIssues   GitHubWriteGroup = "issues"   // create and edit issues, comments, labels
	GitHubWritePulls    GitHubWriteGroup = "pulls"    // PR comments, reviews, and edits of a PR's title, body and labels
	GitHubWriteProjects GitHubWriteGroup = "projects" // add items to a project and set an item's fields
)

// AllGitHubWriteGroups lists the groups in the order the settings and the stored value show them.
var AllGitHubWriteGroups = []GitHubWriteGroup{GitHubWriteIssues, GitHubWritePulls, GitHubWriteProjects}

// GitHubWriteAllRepos stands in a setting's repos for every repository the room lists when a request is made.
const GitHubWriteAllRepos = "*"

// GitHubWrites is a room's githubWrites setting, or what it comes to once `*` is replaced by the room's repositories. No
// groups means the setting is off. Repos hold repositories of the room, or `*`, in the order the owner gave.
type GitHubWrites struct {
	Repos  []RepoName         `json:"repos"`
	Groups []GitHubWriteGroup `json:"groups"`
}

// On says whether any write is allowed.
func (w GitHubWrites) On() bool { return len(w.Groups) > 0 && len(w.Repos) > 0 }

// Has says whether a group is on.
func (w GitHubWrites) Has(group GitHubWriteGroup) bool { return slices.Contains(w.Groups, group) }

// Allows says whether a repository is among the repos, compared the way GitHub names repositories, without case.
func (w GitHubWrites) Allows(repo string) bool {
	return slices.ContainsFunc(w.Repos, func(each RepoName) bool { return strings.EqualFold(string(each), repo) })
}

// Expanded is these values with `*` replaced by the room's repositories, which are the only ones it ever names.
func (w GitHubWrites) Expanded(room []RoomRepo) GitHubWrites {
	if !slices.Contains(w.Repos, RepoName(GitHubWriteAllRepos)) {
		return w
	}
	w.Repos = nil
	for _, repo := range room {
		w.Repos = append(w.Repos, repo.Repo)
	}
	return w
}

// GitHubWriteReason says why the githubWrites setting refused something, so a caller reads the cause and no message.
type GitHubWriteReason string

const (
	// Refusals of a change to the setting.
	GitHubWriteOwnerOnly     GitHubWriteReason = "owner_only"       // only the owner changes it
	GitHubWriteRepoNotInRoom GitHubWriteReason = "repo_not_in_room" // a repository the room does not list
	GitHubWriteUnknownGroup  GitHubWriteReason = "unknown_group"    // a group that does not exist
	// Refusals of a request to GitHub.
	GitHubWriteOff                 GitHubWriteReason = "writes_off"                   // the room allows no write
	GitHubWriteGroupOff            GitHubWriteReason = "group_off"                    // the write belongs to a group that is off
	GitHubWriteRouteNotAllowed     GitHubWriteReason = "route_not_allowed"            // a REST method and path outside the table
	GitHubWriteRepoNotAllowed      GitHubWriteReason = "repo_not_allowed"             // a repository, or a project's owner, outside the allowed set
	GitHubWriteFieldNotAllowed     GitHubWriteReason = "field_not_allowed"            // a body or input field outside the route's list
	GitHubWriteContentType         GitHubWriteReason = "content_type"                 // a body that is not application/json
	GitHubWriteBodyUnreadable      GitHubWriteReason = "body_unreadable"              // a body that is too large or does not parse
	GitHubWriteGraphQLPersisted    GitHubWriteReason = "graphql_persisted"            // no query text: a persisted query or extensions
	GitHubWriteGraphQLBatch        GitHubWriteReason = "graphql_batch"                // an array of operations
	GitHubWriteGraphQLSubscription GitHubWriteReason = "graphql_subscription"         // a subscription
	GitHubWriteGraphQLUnparsable   GitHubWriteReason = "graphql_unparsable"           // a document that does not parse
	GitHubWriteGraphQLMutation     GitHubWriteReason = "graphql_mutation_not_allowed" // a mutation outside the table
	GitHubWriteGraphQLShape        GitHubWriteReason = "graphql_shape"                // a mutation not written as field(input: $variable)
	GitHubWriteGraphQLUnresolved   GitHubWriteReason = "graphql_unresolved"           // a node ID GitHub did not resolve to a repository
	GitHubWriteIDUnresolved        GitHubWriteReason = "id_unresolved"                // a REST id (a comment, a project item) GitHub did not resolve to an allowed repository
	GitHubWriteReviewEvent         GitHubWriteReason = "review_event_not_allowed"     // a review event other than COMMENT or REQUEST_CHANGES, such as APPROVE
)

// GitHubWritesRefused is the cause of a call that the githubWrites setting refused.
type GitHubWritesRefused struct {
	Reason GitHubWriteReason `json:"reason"`
}

// AllowsOwner says whether an owner, a user or an organization, owns a repository of the allowed set. A project belongs to an
// owner and not to a repository, so projects are held to this.
func (w GitHubWrites) AllowsOwner(owner string) bool {
	return slices.ContainsFunc(w.Repos, func(each RepoName) bool {
		name, _, _ := strings.Cut(string(each), "/")
		return strings.EqualFold(name, owner)
	})
}

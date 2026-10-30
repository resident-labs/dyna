package room

const FamilyGitHubWrite EventFamily = "github-write"
const TypeGitHubWriteRefused EventType = "refused"

// GitHubWriteVerb names the parsed GitHub operation.
type GitHubWriteVerb string

var gitHubWriteVerbs = []GitHubWriteVerb{"issue.create", "issue.edit", "issue.comment", "issue.close", "issue.reopen", "pr.create", "pr.edit", "pr.ready", "pr.comment", "pr.review.comment", "pr.review.verdict", "pr.close", "pr.reopen", "comment.edit", "comment.delete", "run.rerun.failed", "run.rerun", "run.cancel", "repo.fork", "unknown"}

// GitHubWriteRule names the policy row that refused a write.
type GitHubWriteRule string

// GitHubWriteReason names a policy refusal.
type GitHubWriteReason string

const (
	GitHubWriteReadOnly       GitHubWriteReason = "read-only-room"
	GitHubWriteOther          GitHubWriteReason = "not-room-repository"
	GitHubWriteBranchDelete   GitHubWriteReason = "upstream-branch-delete"
	GitHubWriteForeignItem    GitHubWriteReason = "not-room-account-item"
	GitHubWriteForeignComment GitHubWriteReason = "foreign-comment"
	GitHubWriteForeignHead    GitHubWriteReason = "upstream-edit-foreign-head"
	GitHubWriteUpstreamVerb   GitHubWriteReason = "upstream-verb-not-allowed"
)

var gitHubWriteReasons = []GitHubWriteReason{GitHubWriteReadOnly, GitHubWriteOther, GitHubWriteBranchDelete, GitHubWriteForeignItem, GitHubWriteForeignComment, GitHubWriteForeignHead, GitHubWriteUpstreamVerb}

type GitHubWriteRefusedEvent struct {
	Requester  string            `json:"requester"`
	Verb       GitHubWriteVerb   `json:"verb"`
	Repository string            `json:"repository"`
	Reason     GitHubWriteReason `json:"reason"`
	Rule       GitHubWriteRule   `json:"rule"`
}

func (GitHubWriteRefusedEvent) eventFamily() EventFamily { return FamilyGitHubWrite }
func (GitHubWriteRefusedEvent) eventType() EventType     { return TypeGitHubWriteRefused }
func (e GitHubWriteRefusedEvent) eventSubject() string   { return e.Requester }
func (GitHubWriteRefusedEvent) eventDedupe() string      { return "" }

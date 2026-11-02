package contract

// GitHubKind says what a GitHub number is.
type GitHubKind string

const (
	GitHubIssue GitHubKind = "issue"
	GitHubPR    GitHubKind = "pr"
)

// GitHubAction is what happened on an item, in GitHub's own word where it has one.
type GitHubAction string

const (
	GitHubOpened               GitHubAction = "opened"
	GitHubEdited               GitHubAction = "edited" // the body changed; GitHub does not say by whom
	GitHubCommented            GitHubAction = "commented"
	GitHubReviewCommented      GitHubAction = "review_commented"
	GitHubReviewed             GitHubAction = "reviewed"
	GitHubClosed               GitHubAction = "closed"
	GitHubReopened             GitHubAction = "reopened"
	GitHubMerged               GitHubAction = "merged"
	GitHubLabeled              GitHubAction = "labeled"
	GitHubUnlabeled            GitHubAction = "unlabeled"
	GitHubAssigned             GitHubAction = "assigned"
	GitHubUnassigned           GitHubAction = "unassigned"
	GitHubMilestoned           GitHubAction = "milestoned"
	GitHubDemilestoned         GitHubAction = "demilestoned"
	GitHubRenamed              GitHubAction = "renamed"
	GitHubReviewRequested      GitHubAction = "review_requested"
	GitHubReviewRequestRemoved GitHubAction = "review_request_removed"
	GitHubConvertedToDraft     GitHubAction = "convert_to_draft"
	GitHubReadyForReview       GitHubAction = "ready_for_review"
	GitHubForcePushed          GitHubAction = "head_ref_force_pushed"
	GitHubMentioned            GitHubAction = "mentioned" // Actor is the login mentioned, as in GitHub's feed
)

// GitHubItem is one thing a GitHub feed showed. Detail is the small fact that goes with Action: a label, a milestone, a new
// title, or the login assigned or asked to review. Text is a comment or a body. Outside marks Actor as anyone but the owner's
// GitHub account, so Detail and Text reach a session wrapped (decision 13).
type GitHubItem struct {
	Repo    RepoName     `json:"repo"`
	Number  int          `json:"number"`
	Kind    GitHubKind   `json:"kind"`
	Action  GitHubAction `json:"action"`
	Actor   string       `json:"actor"`
	Author  string       `json:"author,omitempty"` // of the item, when the feed says
	URL     string       `json:"url"`
	Detail  string       `json:"detail,omitempty"`
	Text    string       `json:"text,omitempty"`
	Outside bool         `json:"outside"`
}

func (GitHubItem) EventType() EventType { return "github.item" }
func (GitHubItem) isEventBody()         {}

// OwnedBy is empty: a GitHubItem is about something no session tracks, or it would be in that session's change.
func (GitHubItem) OwnedBy() SessionID { return "" }

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
	GitHubMentioned            GitHubAction = "mentioned" // of the owner's account, and Actor is who wrote it
)

// GitHubItem is one thing a GitHub feed showed. Detail is the small fact that goes with Action: a label, a milestone, a new
// title, or the login assigned or asked to review. Text is a comment or a body. Outside marks Actor as anyone but the owner's
// GitHub account, so Detail and Text are untrusted: they reach a session wrapped (decision 13), and a Run sees this flag.
// Session names the owner of a tracked item.
type GitHubItem struct {
	Repo    RepoName     `json:"repo"`
	Number  int          `json:"number"`
	Kind    GitHubKind   `json:"kind,omitempty"`
	Action  GitHubAction `json:"action,omitempty"`
	Session SessionID    `json:"session,omitempty"`
	Actor   string       `json:"actor,omitempty"`
	Author  string       `json:"author,omitempty"` // of the item, when the feed says
	URL     string       `json:"url"`
	Detail  string       `json:"detail,omitempty"`
	Text    string       `json:"text,omitempty"`
	Outside bool         `json:"outside,omitempty"`
}

// PrOpened is a pull request opened, by a session or on GitHub.
type PrOpened GitHubItem

// PrMergedEvent is a pull request merged.
type PrMergedEvent GitHubItem

// PrClosedEvent is a pull request closed without merging.
type PrClosedEvent GitHubItem

// IssueOpenedEvent is an issue opened.
type IssueOpenedEvent GitHubItem

// IssueClosedEvent is an issue closed.
type IssueClosedEvent GitHubItem

// GitHubComment is a new comment on an issue or a pull request, inline ones included.
type GitHubComment GitHubItem

// GitHubLabel is a label added to or taken off an issue or a pull request.
type GitHubLabel GitHubItem

// GitHubMention is the owner's GitHub account mentioned on an issue or a pull request. Its actor is who wrote the mention.
type GitHubMention GitHubItem

func (PrOpened) EventType() EventType         { return "github.pr.opened" }
func (PrOpened) isEventBody()                 {}
func (PrMergedEvent) EventType() EventType    { return "github.pr.merged" }
func (PrMergedEvent) isEventBody()            {}
func (PrClosedEvent) EventType() EventType    { return "github.pr.closed" }
func (PrClosedEvent) isEventBody()            {}
func (IssueOpenedEvent) EventType() EventType { return "github.issue.opened" }
func (IssueOpenedEvent) isEventBody()         {}
func (IssueClosedEvent) EventType() EventType { return "github.issue.closed" }
func (IssueClosedEvent) isEventBody()         {}
func (GitHubComment) EventType() EventType    { return "github.comment" }
func (GitHubComment) isEventBody()            {}
func (GitHubLabel) EventType() EventType      { return "github.label" }
func (GitHubLabel) isEventBody()              {}
func (GitHubMention) EventType() EventType    { return "github.mention" }
func (GitHubMention) isEventBody()            {}

// Item gives an event of one item its record back.
func (e PrOpened) Item() GitHubItem         { return GitHubItem(e) }
func (e PrMergedEvent) Item() GitHubItem    { return GitHubItem(e) }
func (e PrClosedEvent) Item() GitHubItem    { return GitHubItem(e) }
func (e IssueOpenedEvent) Item() GitHubItem { return GitHubItem(e) }
func (e IssueClosedEvent) Item() GitHubItem { return GitHubItem(e) }
func (e GitHubComment) Item() GitHubItem    { return GitHubItem(e) }
func (e GitHubLabel) Item() GitHubItem      { return GitHubItem(e) }
func (e GitHubMention) Item() GitHubItem    { return GitHubItem(e) }

// Event is the record as a typed event, or nil if subscribers have no event for it. Actions with no event only reach an owner.
func (i GitHubItem) Event() EventBody {
	switch i.Action {
	case GitHubCommented, GitHubReviewCommented:
		return GitHubComment(i)
	case GitHubLabeled, GitHubUnlabeled:
		return GitHubLabel(i)
	case GitHubMentioned:
		return GitHubMention(i)
	case GitHubMerged:
		return PrMergedEvent(i)
	case GitHubOpened:
		if i.Kind == GitHubPR {
			return PrOpened(i)
		}
		return IssueOpenedEvent(i)
	case GitHubClosed:
		if i.Kind == GitHubPR {
			return PrClosedEvent(i)
		}
		return IssueClosedEvent(i)
	}
	return nil
}

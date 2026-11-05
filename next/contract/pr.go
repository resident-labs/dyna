package contract

import "slices"

const (
	MethodPrList    Method = "pr.list"
	MethodPrStatus  Method = "pr.status"
	MethodPrTrack   Method = "pr.track"
	MethodPrUntrack Method = "pr.untrack"
)

// PrState is whether a tracked PR is still open on GitHub.
type PrState string

const (
	PrOpen   PrState = "open"
	PrMerged PrState = "merged"
	PrClosed PrState = "closed"
)

// PrReview is GitHub's review decision.
type PrReview string

const (
	PrNoReview         PrReview = "" // nobody with write access has reviewed
	PrApproved         PrReview = "approved"
	PrChangesRequested PrReview = "changes_requested"
	PrReviewRequired   PrReview = "review_required"
)

// PrCI is the head commit's check rollup.
type PrCI string

const (
	PrCIUnread  PrCI = "" // no read has found it yet
	PrCINone    PrCI = "none"
	PrCIPending PrCI = "pending"
	PrCISuccess PrCI = "success"
	PrCIFailure PrCI = "failure"
)

// PrMerge is whether GitHub could merge the PR.
type PrMerge string

const (
	PrMergeUnknown  PrMerge = "" // GitHub has not worked it out yet
	PrMergeClean    PrMerge = "clean"
	PrMergeBlocked  PrMerge = "blocked"
	PrMergeBehind   PrMerge = "behind"
	PrMergeConflict PrMerge = "conflict"
)

// PrChangeKind is one change that wakes the PR's owning session, except PrChangePushed, which tells subscribers only.
type PrChangeKind string

const (
	PrChangeCIFailed         PrChangeKind = "ci_failed"
	PrChangeCIPassed         PrChangeKind = "ci_passed"
	PrChangeChangesRequested PrChangeKind = "changes_requested"
	PrChangeApproved         PrChangeKind = "approved"
	PrChangeConflict         PrChangeKind = "conflict"
	PrChangeMerged           PrChangeKind = "merged"
	PrChangeClosed           PrChangeKind = "closed"
	PrChangeComment          PrChangeKind = "comment"
	PrChangeMetadata         PrChangeKind = "metadata"
	PrChangePushed           PrChangeKind = "pushed" // the head moved and Push has the shas; the owner made the push, so only subscribers hear it
)

// PrPush is a tracked PR's head moving, as one pass read it. Before is the head last stored, so pushes between two reads are one move.
type PrPush struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// PrSummary is what Dyna last read about a tracked PR. Repo is the PR's base repository, which may not be a room repository.
type PrSummary struct {
	Repo    RepoName  `json:"repo"`
	Number  int       `json:"number"`
	URL     string    `json:"url"`
	Title   string    `json:"title"`
	Author  string    `json:"author"`
	Session SessionID `json:"session"`
	Draft   bool      `json:"draft"`
	State   PrState   `json:"state"`
	Head    string    `json:"head"`
	Review  PrReview  `json:"review"`
	CI      PrCI      `json:"ci"`
	Merge   PrMerge   `json:"merge"`
}

type PrListRequest struct {
	Room RoomID `json:"room,omitempty"` // a session's own room when empty; the owner's empty room means every room
	All  bool   `json:"all,omitempty"`  // also merged and closed PRs
}

type PrListResponse struct {
	PRs []PrSummary `json:"prs"`
}

type PrStatusRequest struct {
	Repo   RepoName `json:"repo,omitempty"` // the session's repository when empty
	Number int      `json:"number"`
}

type PrStatusResponse struct {
	PR PrSummary `json:"pr"`
}

// PrTrackRequest adds a PR. A session tracks for itself. The owner names a session, or a room for its root.
type PrTrackRequest struct {
	Repo    RepoName  `json:"repo,omitempty"` // the session's repository when empty
	Number  int       `json:"number"`
	Room    RoomID    `json:"room,omitempty"`
	Session SessionID `json:"session,omitempty"`
}

type PrTrackResponse struct {
	PR PrSummary `json:"pr"`
}

type PrUntrackRequest struct {
	Repo   RepoName `json:"repo,omitempty"` // the session's repository when empty
	Number int      `json:"number"`
}

type PrUntrackResponse struct{}

// PrComment is a comment or review body. Outside marks text from anyone but the owner's GitHub account.
type PrComment struct {
	Author  string `json:"author"`
	Body    string `json:"body"`
	URL     string `json:"url"`
	Outside bool   `json:"outside"`
}

// PrChange is what one pass saw on one PR. No Kinds means the state moved without anything to wake for, such as CI starting.
// Actions are what people did on it, oldest first, each with its actor.
type PrChange struct {
	PR           PrSummary      `json:"pr"`
	Kinds        []PrChangeKind `json:"kinds,omitempty"`
	FailedChecks []string       `json:"failedChecks,omitempty"` // named by the workflow files at the PR's head
	Outside      bool           `json:"outside"`                // the PR comes from a fork of an author who is not the owner's GitHub account, so the head's workflow files, and the check names in them, are the author's
	Comments     []PrComment    `json:"comments,omitempty"`
	MoreComments int            `json:"moreComments,omitempty"`
	Actions      []GitHubItem   `json:"actions,omitempty"`
	Push         *PrPush        `json:"push,omitempty"` // set exactly when Kinds has PrChangePushed
}

// PrChanged is one pass's changes on the tracked PRs of one session. Issues holds its issues that moved in the same pass. A subscriber also hears a push (kind pushed) with its before and after head, which the owner does not.
type PrChanged struct {
	Session SessionID     `json:"session"`
	Changes []PrChange    `json:"changes"`
	Issues  []IssueChange `json:"issues,omitempty"`
}

func (PrChanged) EventType() EventType { return "github.pr.changed" }
func (PrChanged) isEventBody()         {}

// CIFinished is one CI result on a tracked PR's head. It states what github.pr.changed told the owner, for anyone else who subscribes. Result is success or failure.
type CIFinished struct {
	Repo    RepoName  `json:"repo"`
	Number  int       `json:"number"`
	Session SessionID `json:"session"`
	URL     string    `json:"url"`
	Head    string    `json:"head"`
	Result  PrCI      `json:"result"`
}

// GitMainUpdated is a repository's default branch moving. No session owns it.
type GitMainUpdated struct {
	Repo RepoName `json:"repo"`
	Head string   `json:"head"`
}

// CIRun is one check run or status in a commit's rollup, as GitHub names it. Conclusion is GitHub's own word for how it ended, empty while it runs.
type CIRun struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

// GitMainCIFinished is the CI of a default-branch head finishing, once per head, with repo, branch, sha, result and runs; where kind is success or failure. It states a head no tracked PR owns, which github.ci.finished never does.
type GitMainCIFinished struct {
	Repo   RepoName `json:"repo"`
	Branch string   `json:"branch"`
	Sha    string   `json:"sha"`
	Result PrCI     `json:"result"`
	Runs   []CIRun  `json:"runs"`
}

func (CIFinished) EventType() EventType        { return "github.ci.finished" }
func (CIFinished) isEventBody()                {}
func (GitMainUpdated) EventType() EventType    { return "git.main_updated" }
func (GitMainUpdated) isEventBody()            {}
func (GitMainCIFinished) EventType() EventType { return "git.main_ci_finished" }
func (GitMainCIFinished) isEventBody()         {}

// Waking reports whether any change is news to someone: the owner, or a subscriber, who also hears pushes.
func (b PrChanged) Waking() bool {
	for _, change := range b.Changes {
		if len(change.Kinds) > 0 {
			return true
		}
	}
	return issuesWake(b.Issues)
}

// WakesOwner reports whether any change needs the owning session. A push is its own act, so a push alone does not.
func (b PrChanged) WakesOwner() bool {
	for _, change := range b.Changes {
		if slices.ContainsFunc(change.Kinds, func(kind PrChangeKind) bool { return kind != PrChangePushed }) {
			return true
		}
	}
	return issuesWake(b.Issues)
}

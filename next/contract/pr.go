package contract

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
	PrCIUnread  PrCI = "" // no poll has read it yet
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

// PrChangeKind is one change that wakes the PR's owning session.
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
)

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

// PrChange is what one poll saw on one PR. No Kinds means the state moved without anything to wake for, such as CI starting.
type PrChange struct {
	PR           PrSummary      `json:"pr"`
	Kinds        []PrChangeKind `json:"kinds,omitempty"`
	FailedChecks []string       `json:"failedChecks,omitempty"` // named by workflow files, so they count as text from the PR's author
	Outside      bool           `json:"outside"`                // the PR's author is not the owner's GitHub account
	Comments     []PrComment    `json:"comments,omitempty"`
	MoreComments int            `json:"moreComments,omitempty"`
	Meta         *MetaChange    `json:"meta,omitempty"`
}

// PrChanged batches one poll's changes for the session that owns those PRs. Issues holds its issues that moved in the same pass.
type PrChanged struct {
	Session SessionID     `json:"session"`
	Changes []PrChange    `json:"changes"`
	Issues  []IssueChange `json:"issues,omitempty"`
}

func (PrChanged) EventType() EventType { return "pr.changed" }
func (PrChanged) isEventBody()         {}

// PrMergedEvent, PrClosedEvent and CIFinished state what pr.changed already told the owner, for anyone else who subscribes.
// They carry no delivery of their own, and their owner keeps the default subscription from waking root (spec 7.6).
type PrMergedEvent struct {
	Repo    RepoName  `json:"repo"`
	Number  int       `json:"number"`
	Session SessionID `json:"session"`
	URL     string    `json:"url"`
}

type PrClosedEvent struct {
	Repo    RepoName  `json:"repo"`
	Number  int       `json:"number"`
	Session SessionID `json:"session"`
	URL     string    `json:"url"`
}

// CIFinished is one CI result on a PR's head. Result is success or failure.
type CIFinished struct {
	Repo    RepoName  `json:"repo"`
	Number  int       `json:"number"`
	Session SessionID `json:"session"`
	URL     string    `json:"url"`
	Head    string    `json:"head"`
	Result  PrCI      `json:"result"`
}

// RepoMainUpdated says a repository's default branch moved. No session owns it.
type RepoMainUpdated struct {
	Repo RepoName `json:"repo"`
	Head string   `json:"head"`
}

func (PrMergedEvent) EventType() EventType   { return "pr.merged" }
func (PrMergedEvent) isEventBody()           {}
func (PrClosedEvent) EventType() EventType   { return "pr.closed" }
func (PrClosedEvent) isEventBody()           {}
func (CIFinished) EventType() EventType      { return "ci.finished" }
func (CIFinished) isEventBody()              {}
func (RepoMainUpdated) EventType() EventType { return "repo.main_updated" }
func (RepoMainUpdated) isEventBody()         {}

func (e PrMergedEvent) OwnedBy() SessionID { return e.Session }
func (e PrClosedEvent) OwnedBy() SessionID { return e.Session }
func (e CIFinished) OwnedBy() SessionID    { return e.Session }
func (RepoMainUpdated) OwnedBy() SessionID { return "" }

// Waking reports whether any change needs the session's attention.
func (b PrChanged) Waking() bool {
	for _, change := range b.Changes {
		if len(change.Kinds) > 0 {
			return true
		}
	}
	return issuesWake(b.Issues)
}

package contract

const (
	MethodIssueList    Method = "issue.list"
	MethodIssueStatus  Method = "issue.status"
	MethodIssueTrack   Method = "issue.track"
	MethodIssueUntrack Method = "issue.untrack"
)

// IssueState is whether a tracked issue is still open on GitHub.
type IssueState string

const (
	IssueOpen   IssueState = "open"
	IssueClosed IssueState = "closed"
)

// IssueSummary is what Dyna last read about a tracked issue. Labels, Assignees and Milestone are as last told to the session.
type IssueSummary struct {
	Repo      RepoName   `json:"repo"`
	Number    int        `json:"number"`
	URL       string     `json:"url"`
	Title     string     `json:"title"`
	Author    string     `json:"author"`
	Session   SessionID  `json:"session"`
	State     IssueState `json:"state"`
	Labels    []string   `json:"labels,omitempty"`
	Assignees []string   `json:"assignees,omitempty"`
	Milestone string     `json:"milestone,omitempty"`
}

type IssueListRequest struct {
	Room RoomID `json:"room,omitempty"` // a session's own room when empty; the owner's empty room means every room
	All  bool   `json:"all,omitempty"`  // also closed issues
}

type IssueListResponse struct {
	Issues []IssueSummary `json:"issues"`
}

type IssueStatusRequest struct {
	Repo   RepoName `json:"repo,omitempty"` // the session's repository when empty
	Number int      `json:"number"`
}

type IssueStatusResponse struct {
	Issue IssueSummary `json:"issue"`
}

// IssueTrackRequest adds an issue. A session tracks for itself. The owner names a session, or a room for its root.
type IssueTrackRequest struct {
	Repo    RepoName  `json:"repo,omitempty"` // the session's repository when empty
	Number  int       `json:"number"`
	Room    RoomID    `json:"room,omitempty"`
	Session SessionID `json:"session,omitempty"`
}

type IssueTrackResponse struct {
	Issue IssueSummary `json:"issue"`
}

type IssueUntrackRequest struct {
	Repo   RepoName `json:"repo,omitempty"` // the session's repository when empty
	Number int      `json:"number"`
}

type IssueUntrackResponse struct{}

// IssueChange is what one pass saw on one issue. Kinds are closed, comment or metadata, and no Kinds means the row moved
// without anything to wake for. Actions are what people did on it, oldest first, each with its actor.
type IssueChange struct {
	Issue        IssueSummary   `json:"issue"`
	Kinds        []PrChangeKind `json:"kinds,omitempty"`
	Outside      bool           `json:"outside"` // the issue's author is not the owner's GitHub account
	Comments     []PrComment    `json:"comments,omitempty"`
	MoreComments int            `json:"moreComments,omitempty"`
	Actions      []GitHubItem   `json:"actions,omitempty"`
}

// IssueChanged batches one poll's changes for the session that owns those issues. Every pass that moved an issue has one.
// A session whose PRs moved in the same pass hears of its issues in PrChanged.Issues instead, because one pass gives a
// session one delivery, and this event then carries none of its own: it is there for subscribers.
type IssueChanged struct {
	Session SessionID     `json:"session"`
	Changes []IssueChange `json:"changes"`
}

func (IssueChanged) EventType() EventType { return "issue.changed" }
func (IssueChanged) isEventBody()         {}

// IssueClosedEvent states what the issue's change already told its owner, for anyone else who subscribes, as PrClosedEvent does.
type IssueClosedEvent struct {
	Repo    RepoName  `json:"repo"`
	Number  int       `json:"number"`
	Session SessionID `json:"session"`
	URL     string    `json:"url"`
}

func (IssueClosedEvent) EventType() EventType { return "issue.closed" }
func (IssueClosedEvent) isEventBody()         {}

func (e IssueClosedEvent) OwnedBy() SessionID { return e.Session }

// Waking reports whether any change needs the session's attention.
func (b IssueChanged) Waking() bool { return issuesWake(b.Changes) }

func issuesWake(changes []IssueChange) bool {
	for _, change := range changes {
		if len(change.Kinds) > 0 {
			return true
		}
	}
	return false
}

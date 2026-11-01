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

// MetaChange is what moved on a PR's or an issue's metadata since the session was last told. Title, Milestone and the labels
// are free text, so they reach a session wrapped when the item's author is not the owner's GitHub account. Logins are not.
type MetaChange struct {
	Title            string   `json:"title,omitempty"` // the new title, when it changed
	LabelsAdded      []string `json:"labelsAdded,omitempty"`
	LabelsRemoved    []string `json:"labelsRemoved,omitempty"`
	Assigned         []string `json:"assigned,omitempty"`
	Unassigned       []string `json:"unassigned,omitempty"`
	Milestone        string   `json:"milestone,omitempty"` // the new milestone, when it changed to one
	MilestoneRemoved bool     `json:"milestoneRemoved,omitempty"`
	BodyEdited       bool     `json:"bodyEdited,omitempty"`
	Reopened         bool     `json:"reopened,omitempty"`
	Draft            bool     `json:"draft,omitempty"` // a PR became a draft
	Ready            bool     `json:"ready,omitempty"` // a PR left draft
	ReviewersAdded   []string `json:"reviewersAdded,omitempty"`
	ReviewersRemoved []string `json:"reviewersRemoved,omitempty"`
}

// IssueChange is what one poll saw on one issue. Kinds are closed, comment or metadata, and no Kinds means the row moved
// without anything to wake for.
type IssueChange struct {
	Issue        IssueSummary   `json:"issue"`
	Kinds        []PrChangeKind `json:"kinds,omitempty"`
	Outside      bool           `json:"outside"` // the issue's author is not the owner's GitHub account
	Comments     []PrComment    `json:"comments,omitempty"`
	MoreComments int            `json:"moreComments,omitempty"`
	Meta         *MetaChange    `json:"meta,omitempty"`
}

// IssueChanged batches one poll's changes for the session that owns those issues. A session whose PRs moved in the same
// pass hears of its issues in PrChanged.Issues instead, because one pass gives a session one event.
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

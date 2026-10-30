package view

import "time"

type GitHubUser struct {
	Login string `json:"login"`
	Name  string `json:"name,omitempty"`
	IsBot bool   `json:"isBot,omitempty"`
}

type GitHubLabel struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// GitHubItem is one pull request or issue row. Issue rows also carry work facts.
type GitHubItem struct {
	WorkingSession     string           `json:"workingSession,omitempty"`
	WorkingSessionName string           `json:"workingSessionName,omitempty"`
	WorkingSessionLive bool             `json:"workingSessionLive"`
	LinkedPRs          []GitHubLinkedPR `json:"linkedPRs,omitempty"`
	Group              string           `json:"group,omitempty"`
	Number             int              `json:"number"`
	Title              string           `json:"title"`
	State              string           `json:"state"`
	StateReason        string           `json:"stateReason,omitempty"`
	IsDraft            bool             `json:"isDraft,omitempty"`
	Author             GitHubUser       `json:"author"`
	Labels             []GitHubLabel    `json:"labels" tstype:"GitHubLabel[] | null"`
	Assignees          []GitHubUser     `json:"assignees,omitempty"`
	CommentCount       int              `json:"commentCount,omitempty"`
	CreatedAt          time.Time        `json:"createdAt"`
	UpdatedAt          time.Time        `json:"updatedAt"`
	ClosedAt           *time.Time       `json:"closedAt,omitempty"`
	MergedAt           *time.Time       `json:"mergedAt,omitempty"`
	URL                string           `json:"url"`
	HeadRefName        string           `json:"headRefName,omitempty"`
	BaseRefName        string           `json:"baseRefName,omitempty"`
	ReviewDecision     string           `json:"reviewDecision,omitempty"`
	Additions          int              `json:"additions,omitempty"`
	Deletions          int              `json:"deletions,omitempty"`
}

type GitHubListResult struct {
	Account string       `json:"account,omitempty"`
	Repo    string       `json:"repo"`
	URL     string       `json:"url"`
	Kind    string       `json:"kind"`
	State   string       `json:"state"`
	Items   []GitHubItem `json:"items" tstype:"GitHubItem[] | null"`
	// More is true when the list hit GitHubListLimit.
	More bool `json:"more"`
}

type GitHubComment struct {
	Author    GitHubUser `json:"author"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"createdAt"`
	URL       string     `json:"url,omitempty"`
}

type GitHubReview struct {
	Author      GitHubUser `json:"author"`
	Body        string     `json:"body"`
	State       string     `json:"state"`
	SubmittedAt time.Time  `json:"submittedAt"`
}

// GitHubCheck joins gh's two kinds of status rows: a CheckRun and a
// commit StatusContext. Result is one of pending, success, failure,
// skipped or neutral.
type GitHubCheck struct {
	Name     string `json:"name"`
	Workflow string `json:"workflow,omitempty"`
	Result   string `json:"result"`
	URL      string `json:"url,omitempty"`
}

type GitHubFile struct {
	Path       string `json:"path"`
	Additions  int    `json:"additions"`
	Deletions  int    `json:"deletions"`
	ChangeType string `json:"changeType"`
}

// GitHubCommit is one commit of a pull request, oldest first.
type GitHubCommit struct {
	SHA      string     `json:"sha"`
	Headline string     `json:"headline"`
	Author   GitHubUser `json:"author"`
	Date     time.Time  `json:"date"`
}

type GitHubDetail struct {
	GitHubItem   `tstype:",extends"`
	Kind         string          `json:"kind"`
	Body         string          `json:"body"`
	Comments     []GitHubComment `json:"comments" tstype:"GitHubComment[] | null"`
	Reviews      []GitHubReview  `json:"reviews" tstype:"GitHubReview[] | null"`
	Checks       []GitHubCheck   `json:"checks" tstype:"GitHubCheck[] | null"`
	Files        []GitHubFile    `json:"files" tstype:"GitHubFile[] | null"`
	Commits      []GitHubCommit  `json:"commits" tstype:"GitHubCommit[] | null"`
	ChangedFiles int             `json:"changedFiles,omitempty"`
	Mergeable    string          `json:"mergeable,omitempty"`
}

type GitHubLinkedPR struct {
	Number  int    `json:"number"`
	State   string `json:"state"`
	IsDraft bool   `json:"isDraft"`
	URL     string `json:"url"`
}

// TrackedIssues is the GitHub half of the Issues tab's Tracked view.
type TrackedIssues struct {
	Items []GitHubItem `json:"items" tstype:"GitHubItem[] | null"`
	// Unread counts issues with live room work that GitHub could not describe;
	// the view offers Retry for them.
	Unread int `json:"unread"`
}

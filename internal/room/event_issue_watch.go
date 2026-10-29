package room

import (
	"strconv"
	"strings"
)

// The issue-watch family reports facts about an upstream GitHub issue that a
// room reads. The room root receives them. Their bodies are GitHub data, so
// the watcher wraps them as external data before they reach an agent.
const FamilyIssueWatch EventFamily = "issue-watch"

// IssueWatchType is the fact an issue-watch event reports. The card picks its
// chip from it.
type IssueWatchType string

// Issue-watch event types.
const (
	IssueWatchComment  IssueWatchType = "comment"
	IssueWatchClosed   IssueWatchType = "closed"
	IssueWatchReopened IssueWatchType = "reopened"
)

// issueWatchTypes lists every issue-watch type the web needs as a union type.
var issueWatchTypes = []IssueWatchType{IssueWatchComment, IssueWatchClosed, IssueWatchReopened}

// IssueWatchEvent tells one new fact about a watched upstream issue. Every
// field is typed, so the card draws the facts without reading the text.
type IssueWatchEvent struct {
	Type IssueWatchType `json:"type"`
	// Repo is the owner and repository, as owner/repo.
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url"`
	// Key identifies the fact: a comment ID, or the issue's update time for a
	// state change. It keeps a repeated state, such as a second close, distinct.
	Key       string `json:"key"`
	Commenter string `json:"commenter,omitempty"`
	// Summary is the short facts line the card and the agent text both start with.
	Summary string `json:"summary"`
	// Body is the comment text, already wrapped as external data.
	Body string `json:"body,omitempty"`
}

func (e IssueWatchEvent) eventFamily() EventFamily { return FamilyIssueWatch }
func (e IssueWatchEvent) eventType() EventType     { return EventType(e.Type) }

// eventSubject is the issue, so every fact of one issue folds into one card.
func (e IssueWatchEvent) eventSubject() string { return e.Repo + "#" + strconv.Itoa(e.Number) }

// eventDedupe keys one post per fact, so a retried read posts it once.
func (e IssueWatchEvent) eventDedupe() string {
	return strings.Join([]string{string(e.Type), e.Repo, strconv.Itoa(e.Number), e.Key}, "\x00")
}

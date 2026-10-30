package state

import "time"

// RecentReview is one row of a room's recent reviewer results, for status.
type RecentReview struct {
	ChangeID string        `json:"changeId,omitempty"`
	PR       int           `json:"pr"`
	Verdict  ReviewVerdict `json:"verdict"`
	At       time.Time     `json:"at"`
}

type AuthorNotes struct {
	Intent    string `json:"intent"`
	Testing   string `json:"testing"`
	Risks     string `json:"risks"`
	Questions string `json:"questions"`
}

type FindingReply struct {
	Disposition string    `json:"disposition"`
	Message     string    `json:"message"`
	Head        string    `json:"head"`
	Author      string    `json:"author"`
	At          time.Time `json:"at"`
}

type Finding struct {
	ID       string          `json:"id"`
	Severity FindingSeverity `json:"severity"`
	File     string          `json:"file"`
	Line     int             `json:"line"`
	Why      string          `json:"why"`
	Fix      string          `json:"fix"`
	Patch    string          `json:"patch,omitempty"`
	Replies  []FindingReply  `json:"replies" tstype:"FindingReply[] | null"`
}

// FastLand records whether an approve-fast result uses direct publication.
// The stored Boolean also preserves the meaning of older review events.
type FastLand struct {
	LowRisk bool `json:"lowRisk"`
}

type ReviewRound struct {
	Reviewer   string        `json:"reviewer,omitempty"`
	ReviewID   int64         `json:"reviewId,omitempty"`
	Round      int           `json:"round"`
	Head       string        `json:"head"`
	Verdict    ReviewVerdict `json:"verdict"`
	Summary    string        `json:"summary"`
	Findings   []Finding     `json:"findings" tstype:"Finding[] | null"`
	StopReason StopReason    `json:"stopReason,omitempty"`
	FastLand   *FastLand     `json:"fastLand,omitempty"`
	At         time.Time     `json:"at"`
}

// Overlap is what another open change in the same repository shares with this
// one. Head and OtherHead identify the compared pair; git runs again only
// when one of them moves.
type Overlap struct {
	ChangeID string `json:"changeId"`
	// Title names a change that has no PR number, such as a local change.
	Title     string   `json:"title,omitempty"`
	Number    int      `json:"number,omitempty"`
	Files     []string `json:"files,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
	Head      string   `json:"head"`
	OtherHead string   `json:"otherHead"`
	MainHead  string   `json:"mainHead,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type MergeTrialStack struct {
	Branch string          `json:"branch"`
	Head   string          `json:"head,omitempty"`
	Count  int             `json:"count"`
	Phase  MergeTrialPhase `json:"phase"`
	Reason string          `json:"reason,omitempty"`
	Logs   string          `json:"logs,omitempty"`
}

type NoiseRule struct {
	ID            string    `json:"id"`
	Host          string    `json:"host"`
	Repo          string    `json:"repo"`
	Bot           string    `json:"bot"`
	Template      string    `json:"template"`
	Reason        string    `json:"reason"`
	SourceURL     string    `json:"sourceUrl"`
	SourceComment string    `json:"sourceComment"`
	LearnedBy     string    `json:"learnedBy,omitempty"`
	Created       time.Time `json:"created"`
	Hits          int       `json:"hits"`
	LastMatched   time.Time `json:"lastMatched,omitempty"`
}

// HeldComment is stored in the PR record in SQLite until the owner admits it.
type HeldComment struct {
	ID        string `json:"id"`
	Commenter string `json:"commenter"`
	Body      string `json:"body"`
	URL       string `json:"url"`
	Version   string `json:"version"`
}

type CheckSummary struct {
	Node                  string `json:"node,omitempty"`
	DurationMs            int64  `json:"durationMs,omitempty"`
	ID                    int64  `json:"id,omitempty"`
	Completed             string `json:"completed,omitempty"`
	FailureExcerpt        string `json:"failureExcerpt,omitempty"`
	FailureExcerptChecked bool   `json:"failureExcerptChecked,omitempty"`
	Name                  string `json:"name"`
	State                 string `json:"state"`
	Status                string `json:"status"`
	Conclusion            string `json:"conclusion"`
	URL                   string `json:"url"`
	CommentURL            string `json:"commentUrl,omitempty"`
}

type BotReview struct {
	Bot      string `json:"bot"`
	State    string `json:"state"`
	Findings int    `json:"findings"`
	URL      string `json:"url"`
}

// IssueRef is one issue that a merge closes automatically.
type IssueRef struct {
	Kind   IssueKind `json:"kind"`
	Number int       `json:"number"`
}

// IssueKind names the tracker that holds a linked issue number.
type IssueKind string

const (
	IssueKindDyna   IssueKind = "dyna"
	IssueKindGitHub IssueKind = "github"
)

// FindingSeverity says how strongly a finding asks for a change.
type FindingSeverity string

// ReviewVerdict is the typed result of one reviewer turn. A reviewer turn ends
// with exactly one of these; the engine rejects any other value.
type ReviewVerdict string

// StopReason is why a reviewer stopped before it judged the change.
type StopReason string

// MergeTrialPhase is the persisted execution phase of a batch or prefix.
type MergeTrialPhase string

// LocalCIConfigState is the local CI configuration a repository has at a head.
// A view reports one of these known states, so a client words it itself and
// never shows a Git or YAML message.
type LocalCIConfigState string

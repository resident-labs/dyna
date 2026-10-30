package gitsvc

import "time"

type GitFileStatus struct {
	Path           string `json:"path"`
	OriginalPath   string `json:"originalPath"`
	Status         string `json:"status"`
	IndexStatus    string `json:"indexStatus"`
	WorktreeStatus string `json:"worktreeStatus"`
	Staged         bool   `json:"staged"`
	Unstaged       bool   `json:"unstaged"`
	Untracked      bool   `json:"untracked"`
	Conflicted     bool   `json:"conflicted"`
}

type GitCommit struct {
	PRNumber  int       `json:"prNumber,omitempty"`
	Hash      string    `json:"hash"`
	ShortHash string    `json:"shortHash"`
	Subject   string    `json:"subject"`
	Author    string    `json:"author"`
	Time      time.Time `json:"time"`
}

type GitStatusResult struct {
	CommitCount  int             `json:"commitCount"`
	ChangedFiles int             `json:"changedFiles"`
	Root         string          `json:"root"`
	Branch       string          `json:"branch"`
	OID          string          `json:"oid"`
	Upstream     string          `json:"upstream"`
	Ahead        int             `json:"ahead"`
	Behind       int             `json:"behind"`
	Files        []GitFileStatus `json:"files" tstype:"GitFileStatus[] | null"`
	Commits      []GitCommit     `json:"commits" tstype:"GitCommit[] | null"`
	Truncated    bool            `json:"truncated"`
	// Worktree is true for a linked git worktree, false for a main checkout.
	Worktree bool `json:"worktree"`
}

type GitDiffResult struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type GitHistoryCommit struct {
	GitCommit `tstype:",extends"`
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Moved     bool   `json:"moved"`
	Path      string `json:"path,omitempty"`
	oldPath   string
}

type GitHistoryResult struct {
	Commits   []GitHistoryCommit `json:"commits" tstype:"GitHistoryCommit[] | null"`
	Earlier   bool               `json:"earlier"`
	Cursor    string             `json:"cursor"`
	Truncated bool               `json:"truncated"`
}

type GitBlameRange struct {
	StartLine int       `json:"startLine"`
	EndLine   int       `json:"endLine"`
	Commit    GitCommit `json:"commit"`
}

type GitBlameResult struct {
	Ranges    []GitBlameRange `json:"ranges" tstype:"GitBlameRange[] | null"`
	Truncated bool            `json:"truncated"`
}

// GitLastCommitsResult omits paths with no commit in the bounded walk.
type GitLastCommitsResult struct {
	Commits   map[string]GitCommit `json:"commits" tstype:"GitCommitsMap"`
	Truncated bool                 `json:"truncated"`
}

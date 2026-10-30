package librarian

import (
	"dyna-agent/internal/library"
	"time"
)

// LibraryView is the read-only feature map the web client shows.
type LibraryView struct {
	Repo    string            `json:"repo"`
	Enabled bool              `json:"enabled"`
	Head    string            `json:"head,omitempty"`
	LastRun time.Time         `json:"lastRun"`
	Entries []library.Entry   `json:"entries"`
	Stale   []string          `json:"stale"`
	Lint    []library.Finding `json:"lint"`
}

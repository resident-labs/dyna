package filesvc

import "time"

type DirectoryEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Kind    string    `json:"kind"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	IsGit   bool      `json:"isGit"`
}

type DirectoryEntries struct {
	Root    string           `json:"root"`
	Path    string           `json:"path"`
	Entries []DirectoryEntry `json:"entries" tstype:"DirectoryEntry[] | null"`
}

type WorkspaceFile struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Language  string `json:"language"`
	Binary    bool   `json:"binary"`
	Text      string `json:"text"`
	URL       string `json:"url"`
	Truncated bool   `json:"truncated"`
}

// BrowseEntry is one folder in a listing. It carries no size, time or mode.
type BrowseEntry struct {
	Name  string `json:"name"`
	IsGit bool   `json:"isGit"`
}

// Browse is one level of folders.
type Browse struct {
	// Parent is the folder that was listed, with ~ expanded.
	Parent    string        `json:"parent"`
	Entries   []BrowseEntry `json:"entries" tstype:"BrowseEntry[] | null"`
	Truncated bool          `json:"truncated,omitempty"`
}

// BrowseInfo tells the picker where to start.
type BrowseInfo struct {
	// Home is the user's home folder. The picker starts there and shows it
	// as ~/.
	Home string `json:"home"`
}

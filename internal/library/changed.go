package library

import (
	"context"
	"strings"

	"dyna-agent/internal/repoid"
)

// Change is one changed file and the live entries that own it. A file with
// no owners is unmapped.
type Change struct {
	File   string   `json:"file"`
	Owners []string `json:"owners"`
}

// Changed lists files that differ between two commits in dir.
func Changed(ctx context.Context, dir, from, to string, entries []Entry) ([]Change, error) {
	files, err := diffNames(ctx, dir, from, to)
	if err != nil {
		return nil, err
	}
	var changes []Change
	for _, f := range files {
		changes = append(changes, Change{File: f, Owners: Owners(entries, f)})
	}
	return changes, nil
}

// Owners returns the ids of live entries that own a file.
func Owners(entries []Entry, file string) []string {
	owners := []string{}
	for _, e := range entries {
		if !e.Retired && e.OwnsFile(file) {
			owners = append(owners, e.ID)
		}
	}
	return owners
}

// Stale returns the ids of live entries whose owned files changed after
// verified_at, or that were never verified. It runs one diff per pin.
func Stale(ctx context.Context, dir string, entries []Entry) (map[string]bool, error) {
	return StaleAt(ctx, dir, "HEAD", entries)
}

// StaleAt is Stale against commit to instead of HEAD.
func StaleAt(ctx context.Context, dir, to string, entries []Entry) (map[string]bool, error) {
	stale := map[string]bool{}
	diffs := map[string][]string{}
	for _, e := range entries {
		if e.Retired {
			continue
		}
		if e.VerifiedAt == "" {
			stale[e.ID] = true
			continue
		}
		files, ok := diffs[e.VerifiedAt]
		if !ok {
			var err error
			if files, err = diffNames(ctx, dir, e.VerifiedAt, to); err != nil {
				return nil, err
			}
			diffs[e.VerifiedAt] = files
		}
		for _, f := range files {
			if e.OwnsFile(f) {
				stale[e.ID] = true
				break
			}
		}
	}
	return stale, nil
}

// diffNames lists the exact names of files that differ between two commits.
// NUL delimiters keep names with spaces, newlines or non-ASCII bytes
// unquoted.
func diffNames(ctx context.Context, dir, from, to string) ([]string, error) {
	out, err := repoid.GitRaw(ctx, dir, "diff", "-z", "--name-only", "--no-renames", from, to, "--")
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// splitNUL splits NUL-terminated Git output and drops empty records.
func splitNUL(out string) []string {
	var names []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			names = append(names, f)
		}
	}
	return names
}

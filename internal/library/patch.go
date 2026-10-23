package library

import (
	"sort"
	"strings"
)

// Patch changes only supplied fields. A pointer to an empty value clears a field.
type Patch struct {
	Name       *string
	Kind       *string
	Parent     *string
	Summary    *string
	Differs    *string
	Owns       *[]string
	Relates    *[]string
	VerifiedAt *string
}

// Apply changes an entry inside the store's atomic update.
func (p Patch) Apply(e *Entry) error {
	for _, field := range []struct {
		from *string
		to   *string
	}{
		{p.Name, &e.Name}, {p.Kind, &e.Kind}, {p.Parent, &e.Parent},
		{p.Summary, &e.Summary}, {p.Differs, &e.Differs}, {p.VerifiedAt, &e.VerifiedAt},
	} {
		if field.from != nil {
			*field.to = strings.TrimSpace(*field.from)
		}
	}
	if p.Owns != nil {
		e.Owns = Union(nil, *p.Owns)
	}
	if p.Relates != nil {
		e.Relates = Union(nil, *p.Relates)
	}
	return nil
}

// Links are docs and tests to add to, or remove from, one entry.
type Links struct {
	Docs  []string `json:"docs,omitempty"`
	Tests []string `json:"tests,omitempty"`
}

// Relink adds add and drops remove, in sorted order without duplicates.
// A removed path that is not linked is ignored, so a repeat run changes nothing.
func Relink(list, add, remove []string) []string {
	drop := map[string]bool{}
	for _, v := range remove {
		if v = strings.TrimSpace(v); v != "" {
			drop[v] = true
		}
	}
	kept := []string{}
	for _, v := range Union(list, add) {
		if !drop[v] {
			kept = append(kept, v)
		}
	}
	return kept
}

// Union combines trimmed paths or entry IDs without duplicates, in sorted order.
func Union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range append(append([]string{}, a...), b...) {
		if v = strings.TrimSpace(v); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

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

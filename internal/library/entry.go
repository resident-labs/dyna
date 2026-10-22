// Package library keeps a repository's feature map: one entry per feature
// with a short summary and the files, docs and tests that implement it.
// Dyna owns the map. It lives in dyna.db and changes only through this
// package, so every write passes the same schema checks.
package library

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Kinds are tags, not structure.
var Kinds = []string{"feature", "adapter", "integration", "service"}

// Entry is one feature in the map.
type Entry struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Parent     string   `json:"parent,omitempty"`
	Summary    string   `json:"summary"`
	Differs    string   `json:"differs,omitempty"`
	Owns       []string `json:"owns"`
	Docs       []string `json:"docs,omitempty"`
	Tests      []string `json:"tests,omitempty"`
	Relates    []string `json:"relates,omitempty"`
	VerifiedAt string   `json:"verifiedAt,omitempty"`
	Retired    bool     `json:"retired,omitempty"`
	Successor  string   `json:"successor,omitempty"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// sentenceEnd counts sentence breaks; a period inside a name such as
// "main.go" is not followed by a space and does not count.
var sentenceEnd = regexp.MustCompile(`[.!?](\s|$)`)

// Sentences returns the number of sentences in a summary.
func Sentences(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n := len(sentenceEnd.FindAllStringIndex(s, -1))
	if !strings.ContainsAny(s[len(s)-1:], ".!?") {
		n++
	}
	return n
}

// validate checks one entry on its own. Checks that need the whole map
// (parent exists, no cycle) are in checkTree.
func (e Entry) validate() error {
	var errs []error
	if !idPattern.MatchString(e.ID) {
		errs = append(errs, fmt.Errorf("id %q must be kebab-case", e.ID))
	}
	if strings.TrimSpace(e.Name) == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if !validKind(e.Kind) {
		errs = append(errs, fmt.Errorf("kind %q must be one of %s", e.Kind, strings.Join(Kinds, ", ")))
	}
	if n := Sentences(e.Summary); n == 0 || n > 2 {
		errs = append(errs, errors.New("summary must be one or two sentences"))
	}
	if e.Parent == e.ID && e.ID != "" {
		errs = append(errs, errors.New("an entry cannot be its own parent"))
	}
	for _, g := range append(append(append([]string{}, e.Owns...), e.Docs...), e.Tests...) {
		if err := validPattern(g); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func validKind(k string) bool {
	for _, v := range Kinds {
		if k == v {
			return true
		}
	}
	return false
}

func validPattern(g string) error {
	if g == "" || path.IsAbs(g) || strings.HasPrefix(path.Clean(g), "..") {
		return fmt.Errorf("path %q must be relative to the repository root", g)
	}
	if _, err := path.Match(strings.ReplaceAll(g, "**", "*"), ""); err != nil {
		return fmt.Errorf("pattern %q: %w", g, err)
	}
	return nil
}

// Match reports whether a slash-separated repository path matches a glob.
// "**" matches any number of directories; other parts use path.Match.
func Match(glob, name string) bool {
	return matchParts(strings.Split(glob, "/"), strings.Split(name, "/"))
}

func matchParts(glob, name []string) bool {
	for len(glob) > 0 {
		if glob[0] == "**" {
			for i := 0; i <= len(name); i++ {
				if matchParts(glob[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(glob[0], name[0]); !ok {
			return false
		}
		glob, name = glob[1:], name[1:]
	}
	return len(name) == 0
}

// OwnsFile reports whether the entry owns a file.
func (e Entry) OwnsFile(name string) bool {
	for _, g := range e.Owns {
		if Match(g, name) {
			return true
		}
	}
	return false
}

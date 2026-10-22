package library

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"dyna-agent/internal/repoid"
)

// MaxSummary is the summary length limit in characters (lint rule 5).
const MaxSummary = 240

// Finding is one lint warning. Lint never blocks in the first version.
type Finding struct {
	Rule    int    `json:"rule"`
	Entry   string `json:"entry,omitempty"`
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}

// sourceExt lists the file types an entry is expected to own.
var sourceExt = map[string]bool{}

// Source reports whether a file is source code that some entry should own.
func Source(name string) bool { return sourceExt[path.Ext(name)] }

func init() {
	for _, e := range strings.Fields(".go .ts .tsx .js .jsx .mjs .cjs .py .rs .java .kt .swift .c .h .cc .cpp .rb .sh .cs .vue .svelte") {
		sourceExt[e] = true
	}
}

// Lint runs the mechanical rules over the live entries of a map against the
// repository checked out at dir. readmeMax is library.readmeMaxLines.
func Lint(ctx context.Context, dir string, entries []Entry, readmeMax int) ([]Finding, error) {
	out, err := repoid.GitRaw(ctx, dir, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	files := splitNUL(out)
	live := map[string]Entry{}
	var rows []Entry
	for _, e := range entries {
		if !e.Retired {
			live[e.ID] = e
			rows = append(rows, e)
		}
	}
	var fs []Finding
	add := func(rule int, entry, file, format string, a ...any) {
		fs = append(fs, Finding{Rule: rule, Entry: entry, File: file, Message: fmt.Sprintf(format, a...)})
	}

	// Rules 1 and 2: each source file has exactly one owner, apart from
	// ancestors and declared relations.
	linked := func(f string) bool {
		for _, e := range rows {
			for _, g := range append(append([]string{}, e.Docs...), e.Tests...) {
				if Match(g, f) {
					return true
				}
			}
		}
		return false
	}
	for _, f := range files {
		owners := Owners(rows, f)
		for i := 0; i < len(owners); i++ {
			for j := i + 1; j < len(owners); j++ {
				a, b := live[owners[i]], live[owners[j]]
				if !ancestor(live, a.ID, b.ID) && !ancestor(live, b.ID, a.ID) && !contains(a.Relates, b.ID) && !contains(b.Relates, a.ID) {
					add(1, a.ID, f, "owned by %s and %s; make one the parent or list them in relates", a.ID, b.ID)
				}
			}
		}
		if len(owners) == 0 && Source(f) && !linked(f) {
			add(2, "", f, "source file is owned by no entry")
		}
	}

	for _, e := range rows {
		// Rule 3: every glob matches a tracked file.
		if len(e.Owns) == 0 {
			add(3, e.ID, "", "entry owns no files")
		}
		for _, g := range e.Owns {
			if !matchesAny(g, files) {
				add(3, e.ID, "", "owns glob %s matches no file", g)
			}
		}
		// Rule 4, map side: linked docs exist and test globs match.
		for _, d := range e.Docs {
			if !exists(dir, d) {
				add(4, e.ID, d, "linked doc does not exist")
			}
		}
		for _, g := range e.Tests {
			if !matchesAny(g, files) {
				add(4, e.ID, "", "tests glob %s matches no file", g)
			}
		}
		// Rule 5: short summary, and differs where same-kind siblings exist.
		if n := len([]rune(e.Summary)); n > MaxSummary || Sentences(e.Summary) > 2 {
			add(5, e.ID, "", "summary is %d characters and %d sentences; the limit is %d characters and 2 sentences", n, Sentences(e.Summary), MaxSummary)
		}
		if e.Parent != "" && strings.TrimSpace(e.Differs) == "" {
			for _, s := range rows {
				if s.ID != e.ID && s.Parent == e.Parent && s.Kind == e.Kind {
					add(5, e.ID, "", "missing differs: sibling %s has the same kind %s", s.ID, e.Kind)
					break
				}
			}
		}
		// Rule 7: the parent exists and the chain has no cycle.
		if e.Parent != "" {
			if _, ok := live[e.Parent]; !ok {
				add(7, e.ID, "", "parent %s does not exist", e.Parent)
			} else if ancestor(live, e.ID, e.ID) {
				add(7, e.ID, "", "parent chain has a cycle")
			}
		}
	}

	// Rule 4, document side: relative links and backticked repository paths
	// in the README and docs/ that no longer exist.
	top := map[string]bool{}
	for _, f := range files {
		top[strings.SplitN(f, "/", 2)[0]] = true
	}
	for _, f := range files {
		if f != "README.md" && !(strings.HasPrefix(f, "docs/") && strings.HasSuffix(f, ".md")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		for _, target := range relativeLinks(string(data)) {
			p := path.Clean(path.Join(path.Dir(f), target))
			if strings.HasPrefix(target, "/") {
				p = path.Clean(strings.TrimPrefix(target, "/"))
			}
			if !exists(dir, p) {
				add(4, "", f, "dead link to %s", target)
			}
		}
		for _, p := range pathMentions(string(data), top) {
			if !exists(dir, p) && wasTracked(ctx, dir, p) {
				add(4, "", f, "mentions %s, which no longer exists", p)
			}
		}
	}

	// Rule 9: the README stays within library.readmeMaxLines.
	if f, over, err := Readme(dir, readmeMax); err != nil {
		return nil, err
	} else if over {
		fs = append(fs, f)
	}

	// Rule 6: owned files changed after the pin.
	stale, err := Stale(ctx, dir, rows)
	if err != nil {
		return nil, err
	}
	for _, e := range rows {
		if !stale[e.ID] {
			continue
		}
		if e.VerifiedAt == "" {
			add(6, e.ID, "", "summary was never verified against a commit")
		} else {
			add(6, e.ID, "", "owned files changed since %s", short(e.VerifiedAt))
		}
	}
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Rule < fs[j].Rule })
	return fs, nil
}

// ancestor reports whether a is a strict ancestor of b. It stops on a cycle,
// so ancestor(m, x, x) reports a cycle through x.
func ancestor(m map[string]Entry, a, b string) bool {
	seen := map[string]bool{}
	for p := m[b].Parent; p != "" && !seen[p]; p = m[p].Parent {
		if p == a {
			return true
		}
		seen[p] = true
		if _, ok := m[p]; !ok {
			return false
		}
	}
	return false
}

var mdLink = regexp.MustCompile(`\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// relativeLinks returns the decoded paths of links that point into the
// repository. Queries and fragments are dropped.
func relativeLinks(md string) []string {
	var out []string
	for _, m := range mdLink.FindAllStringSubmatch(md, -1) {
		u, err := url.Parse(m[1])
		if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "" {
			continue
		}
		out = append(out, u.Path)
	}
	return out
}

var codeSpan = regexp.MustCompile("`([^`\\s]+)`")

// wasTracked reports whether a path was ever in this repository's history.
// A mention of a path that never was, such as a file in another project's
// docs, is not a stale path in this repository.
func wasTracked(ctx context.Context, dir, p string) bool {
	out, err := repoid.Git(ctx, dir, "log", "-1", "--format=%h", "--", p)
	return err == nil && out != ""
}

// pathMentions returns code spans that name a repository path: they contain
// a slash, have no glob or variable, and start with a top-level entry.
func pathMentions(md string, top map[string]bool) []string {
	var out []string
	for _, m := range codeSpan.FindAllStringSubmatch(md, -1) {
		t := m[1]
		if !strings.Contains(t, "/") || strings.ContainsAny(t, "*?$<>{}[]~:") || strings.HasPrefix(t, "/") {
			continue
		}
		p := path.Clean(t)
		if top[strings.SplitN(p, "/", 2)[0]] && !strings.HasPrefix(p, "..") {
			out = append(out, p)
		}
	}
	return out
}

// Readme counts README.md lines in dir and reports a rule 9 finding when
// there are more than max. A missing README is not over.
func Readme(dir string, max int) (Finding, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if os.IsNotExist(err) {
		return Finding{}, false, nil
	}
	if err != nil {
		return Finding{}, false, err
	}
	n := strings.Count(string(data), "\n")
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	f := Finding{Rule: 9, File: "README.md", Message: fmt.Sprintf("README.md has %d lines; library.readmeMaxLines is %d", n, max)}
	return f, max > 0 && n > max, nil
}

func matchesAny(glob string, files []string) bool {
	for _, f := range files {
		if Match(glob, f) {
			return true
		}
	}
	return false
}

func exists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

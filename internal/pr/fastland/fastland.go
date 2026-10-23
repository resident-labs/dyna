// Package fastland decides whether a change's paths are low risk. A reviewer's
// approve-fast result lands fast only when this path rule also agrees. The rule
// is pure, so the decision costs no GitHub call and is easy to table-test.
package fastland

import (
	"path"
	"strings"

	"dyna-agent/internal/pr/state"
)

// Area is the low-risk area of a change. The area names the checks a fast
// landing needs: docs and guide text need none, tests need the test suites.
type Area = state.FastLandArea

// Low-risk areas.
const (
	Docs  = state.FastLandDocs
	Guide = state.FastLandGuide
	Tests = state.FastLandTests
)

// Classify returns the low-risk area of a change and whether every path is low
// risk. An empty change or a single risky path is not low risk. Comments inside
// a source file cannot be proven from a path, so a source file is never low
// risk; only a whole documentation, guide or test path is.
func Classify(paths []string) (Area, bool) {
	if len(paths) == 0 {
		return "", false
	}
	area := Docs
	for _, name := range paths {
		clean, ok := cleanPath(name)
		if !ok {
			return "", false
		}
		// A generated file is never low risk, whatever its extension says.
		if isGenerated(clean) {
			return "", false
		}
		switch {
		case isTest(clean):
			area = Tests
		case isGuide(clean):
			if area != Tests {
				area = Guide
			}
		case isDocs(clean):
		default:
			return "", false
		}
	}
	return area, true
}

// cleanPath normalizes a repo-relative path and rejects a path that is not one.
func cleanPath(name string) (string, bool) {
	clean := strings.TrimSpace(name)
	clean = strings.ReplaceAll(clean, `\`, "/")
	clean = strings.TrimPrefix(clean, "./")
	if clean == "" || strings.HasPrefix(clean, "/") {
		return "", false
	}
	clean = path.Clean(clean)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// isDocs reports a documentation path: a Markdown file anywhere, or any file
// under docs/. A generated file is never low risk.
func isDocs(name string) bool {
	return strings.HasSuffix(name, ".md") || strings.HasPrefix(name, "docs/")
}

// isGuide reports guide text: a Markdown file under internal/guidance. The
// folder also holds Go code that injects the guide, and that code is not text.
func isGuide(name string) bool {
	return strings.HasPrefix(name, "internal/guidance/") && strings.HasSuffix(name, ".md")
}

// isTest reports a test-only path. A test fixture lives with its test.
func isTest(name string) bool {
	return strings.HasSuffix(name, "_test.go") ||
		strings.HasSuffix(name, ".test.ts") ||
		strings.HasPrefix(name, "web/tests/")
}

// isGenerated reports a path whose content is written by a tool, so a human
// review of it does not describe the change.
func isGenerated(name string) bool {
	base := path.Base(name)
	return strings.Contains(base, ".gen.") ||
		strings.Contains(name, "/generated/") ||
		strings.HasPrefix(name, "generated/") ||
		base == "go.sum" || base == "go.mod"
}

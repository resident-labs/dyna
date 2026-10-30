package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestPublicWorkflowSourcesExcludeRetiredAPIs(t *testing.T) {
	paths := []string{
		"../../README.md",
		filepath.Join("..", "..", "internal", "guidance", "skill_body.md"),
		filepath.Join("..", "..", "internal", "subagent", "prompts.go"),
		filepath.Join("..", "..", "internal", "workflow", "prelude.js"),
	}
	guides, err := filepath.Glob(filepath.Join("..", "..", "internal", "cli", "interactive", "guide", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, guides...)

	examples, err := filepath.Glob(filepath.Join("..", "..", "examples", "*.js"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, examples...)

	retired := map[string]*regexp.Regexp{
		"rateProfile() builtin":     regexp.MustCompile(`\brateProfile\s*\(`),
		"profile review CLI":        regexp.MustCompile(`dyna reviews?\b`),
		"worktrees.keep":            regexp.MustCompile(`worktrees\.keep`),
		"string worktree isolation": regexp.MustCompile(`isolation\s*:\s*['"]worktree['"]`),
		"old run disposition":       regexp.MustCompile(`--worktrees=(preserve|auto|all)\b`),
		"keptAt":                    regexp.MustCompile(`\bkeptAt\b`),
		"review() builtin":          regexp.MustCompile(`\breview\(`),
		"subagent wait/send":        regexp.MustCompile(`subagent (wait|send)\b`),
		"runs wait/steer":           regexp.MustCompile(`runs (wait|steer)\b`),
		"dyna events/send/mcp":      regexp.MustCompile(`dyna (events|send|mcp)\b`),
		"workflow mail()":           regexp.MustCompile(`\bmail\s*\(`),
		"orchestrator address":      regexp.MustCompile(`\borch:`),
		"--mine":                    regexp.MustCompile(`--mine\b`),
		"start --wait":              regexp.MustCompile(`--wait(?:$|[^[:alnum:]_-])`),
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for name, pattern := range retired {
			if pattern.Match(data) {
				t.Errorf("%s contains retired %s API token", path, name)
			}
		}
	}
}

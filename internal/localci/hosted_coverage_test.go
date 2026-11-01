package localci

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

// Hosted jobs must map to executable local trial checks with the same filters.
func TestHostedJobsHaveLocalCoverage(t *testing.T) {
	body, err := os.ReadFile("../../.dyna/local-ci.yaml")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := ParseDefinition(body)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Jobs map[string]struct {
			If string `yaml:"if"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(workflow, &doc); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("../../scripts/ci-changes.sh")
	if err != nil {
		t.Fatal(err)
	}
	filters := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^echo "(\w+)=\$\(has '([^']+)'\)"$`).FindAllStringSubmatch(string(script), -1) {
		filters[m[1]] = m[2]
	}
	jobs := map[string]struct {
		suite Suite
		check CheckName
	}{
		"pi": {"pi", "pi"}, "next-pi": {"next-pi", "next-pi"}, "relay": {"relay", "relay"},
		"go-shard": {"go", "go-test-shard (0)"}, "go-static": {"go", "go-static"}, "desktop": {"go", "desktop"},
		"next": {"next", "next"}, "next-extras": {"next", "next-extras"}, "test": {"go", "go-test"}, "frontend-build": {"go", "frontend"},
		"frontend-shard": {"go", "frontend test (0)"}, "frontend": {"go", "frontend"},
		"frontend-check": {"go", "frontend-check"}, "computer-smoke": {"computer-smoke", "computer-smoke"},
	}
	// Changes to the shared command must run its local Pi check.
	checks := definition.RequiredChecks(definition.SuitesForFiles([]string{"scripts/ci.sh"}))
	if !slices.Contains(checks, CheckName("next-pi")) {
		t.Errorf("scripts/ci.sh does not select next-pi: %v", checks)
	}
	condition := regexp.MustCompile(`needs\.changes\.outputs\.(\w+)`)
	for job, spec := range doc.Jobs {
		if job == "changes" || job == "cache-warm" {
			continue
		}
		local, ok := jobs[job]
		if !ok {
			t.Errorf("hosted job %s has no local mapping", job)
			continue
		}
		matches := condition.FindAllStringSubmatch(spec.If, -1)
		if len(matches) != 1 {
			t.Errorf("hosted job %s has unsupported condition %q", job, spec.If)
			continue
		}
		pattern, ok := filters[matches[0][1]]
		if !ok {
			t.Errorf("hosted job %s has unknown filter %s", job, matches[0][1])
			continue
		}
		suite, ok := definition.bySuite[local.suite]
		if !ok {
			t.Errorf("hosted job %s has no suite %s", job, local.suite)
			continue
		}
		found := false
		for _, f := range suite.files {
			if f.String() == pattern {
				found = true
			}
		}
		if !found || !slices.Contains(definition.RequiredChecks([]Suite{local.suite}), local.check) || !slices.Contains(definition.WorkflowSuites(), local.suite) {
			t.Errorf("hosted job %s filter %q lacks local check %s in trial suite %s", job, pattern, local.check, local.suite)
		}
	}
}

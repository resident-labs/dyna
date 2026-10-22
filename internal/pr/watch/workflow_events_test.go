package watch

import (
	"encoding/json"
	"testing"
)

func TestWorkflowPushBranchFilters(t *testing.T) {
	for _, tc := range []struct {
		body, branch string
		want         bool
	}{
		{"on: [push, pull_request]", "dev", true},
		{"on: {push: {branches: ['**/*']}}", "main", true},
		{"on: {push: {branches: ['**/*']}}", "release/main", true},
		{"on: {push: null}", "dev", true},
		{"on: {push: {branches: ['release/**', '!release/private/**', 'release/private/public']}}", "release/v1", true},
		{"on: {push: {branches: ['release/**', '!release/private/**', 'release/private/public']}}", "release/private/v1", false},
		{"on: {push: {branches: ['release/**', '!release/private/**', 'release/private/public']}}", "release/private/public", true},
		{"on: {push: {branches: ['release/*']}}", "release/a/b", false},
		{"on: {push: {branches-ignore: [dev]}}", "dev", false},
		{"on: {push: {branches-ignore: [main]}}", "dev", true},
		{"on: {push: {tags: ['v*']}}", "dev", false},
		{"on: {push: {tags-ignore: ['v*']}}", "dev", false},
		{"on: {push: {paths: ['src/**']}}", "dev", true},
		{"on: {push: {branches: ['v[0-9]+']}}", "v123", true},
	} {
		t.Run(tc.body+"/"+tc.branch, func(t *testing.T) {
			events, err := workflowEvents([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			got, err := pushMatchesBranch(events["push"], tc.branch)
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestIncompleteWorkflowsKeepDefaultGuard(t *testing.T) {
	for _, raw := range []string{
		"", "{}", "null",
		`{"object":null}`,
		`{"object":{"entries":[{"name":"ci.yml","type":"blob","object":{"text":"on: push","isTruncated":true}}]}}`,
		`{"object":{"entries":[{"name":"ci.yml","type":"blob","object":{"text":null}}]}}`,
		`{"object":{"entries":[{"name":"ci.yml","type":"blob","object":{"text":"on: ["}}]}}`,
		`{"object":{"entries":[{"name":"ci.yml","type":"blob","object":{"text":"on: {push: {branches: [42]}}"}}]}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			// null means an absent directory only when the branch was confirmed.
			branch := "dev"
			if raw == "null" {
				branch = ""
			}
			if noDefaultPushCI(json.RawMessage(raw), branch) {
				t.Fatal("incomplete workflow disabled guard")
			}
		})
	}
	for _, raw := range []string{"null", `{"object":{"entries":[]}}`} {
		if !noDefaultPushCI(json.RawMessage(raw), "dev") {
			t.Fatal("absent workflows kept guard active")
		}
	}
}

package prs

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Source: arismoko/horizon run 42393927726, attempt 1, PR 1807,
// head dcb65e8b60b6b2bcdaf97adb65a25cfe38592adf. All 11 job names,
// statuses and conclusions are copied from the Actions jobs endpoint.
// Rollup FAILURE is the incident's reported GitHub rollup state.
func TestHorizon1807Cancelled(t *testing.T) {
	data, err := os.ReadFile("testdata/horizon1807.json")
	if err != nil {
		t.Fatal(err)
	}
	var rollup gqlRollup
	if err := json.Unmarshal(data, &rollup); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []ciRule{ciPullHead, ciMainHead} {
		got, names := summarizeCI(&rollup, gqlSuites{}, rule, 0, time.Now())
		if got != contract.PrCI("cancelled") || !slices.Equal(names, []string{"verify", "pool-watch"}) {
			t.Fatalf("rule %v: %s %v, want cancelled [verify pool-watch]", rule, got, names)
		}
	}
}

func TestCancellationPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name, conclusion, status string
		want                     contract.PrCI
		names                    []string
	}{
		{"cancelled only", "", "", contract.PrCICancelled, []string{"cancelled"}},
		{"success", "SUCCESS", "COMPLETED", contract.PrCICancelled, []string{"cancelled"}},
		{"failure wins", "FAILURE", "COMPLETED", contract.PrCIFailure, []string{"other"}},
		{"incomplete", "", "IN_PROGRESS", contract.PrCIPending, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			nodes := `{"__typename":"CheckRun","name":"cancelled","status":"COMPLETED","conclusion":"CANCELLED"}`
			if tt.status != "" {
				nodes += `,{"__typename":"CheckRun","name":"other","status":"` + tt.status + `","conclusion":"` + tt.conclusion + `"}`
			}
			var r gqlRollup
			if err := json.Unmarshal([]byte(`{"state":"FAILURE","contexts":{"nodes":[`+nodes+`]}}`), &r); err != nil {
				t.Fatal(err)
			}
			for _, rule := range []ciRule{ciMainHead, ciPullHead} {
				got, names := summarizeCI(&r, gqlSuites{}, rule, 0, time.Now())
				if got != tt.want || !slices.Equal(names, tt.names) {
					t.Fatalf("%s %v", got, names)
				}
			}
		})
	}
}

// Shape of sunrise-org/sunrise#3107 at 6f31f19555: pushed with its base, the PR got two events, so dyna-checks ran as 2859 and
// 2860 and modding-book as 1064 and 1065 on one head. Each newer run cancelled the older one and passed, skipping some jobs.
func TestLaterRunReplacesCancelledJob(t *testing.T) {
	suite := func(workflow, event, prs, suiteConclusion string, run int, name, status, conclusion string) string {
		return fmt.Sprintf(`{"__typename":"CheckRun","name":%q,"status":%q,"conclusion":%q,"checkSuite":{"conclusion":%q,"matchingPullRequests":{"nodes":[%s]},"workflowRun":{"event":%q,"runNumber":%d,"workflow":{"id":%q}}}}`, name, status, conclusion, suiteConclusion, prs, event, run, workflow)
	}
	// check is a job of PR 3107; its suite takes the job's conclusion.
	const pr = `{"number":3107}`
	check := func(workflow, event string, run int, name, status, conclusion string) string {
		return suite(workflow, event, pr, conclusion, run, name, status, conclusion)
	}
	const checks, book = "W_checks", "W_book"
	twins := []string{
		check(checks, "pull_request", 2859, "changes", "COMPLETED", "SUCCESS"),
		check(checks, "pull_request", 2859, "verify", "COMPLETED", "CANCELLED"),
		check(checks, "pull_request", 2860, "changes", "COMPLETED", "SUCCESS"),
		check(checks, "pull_request", 2860, "verify", "COMPLETED", "SKIPPED"),
		check(book, "pull_request", 1064, "book", "COMPLETED", "CANCELLED"),
		check(book, "pull_request", 1065, "book", "COMPLETED", "SUCCESS"),
	}
	for _, tt := range []struct {
		name  string
		nodes []string
		want  contract.PrCI
		names []string
	}{
		{"newer runs passed", twins, contract.PrCISuccess, nil},
		{"newer run listed first", []string{twins[5], twins[4]}, contract.PrCISuccess, nil},
		{"newer run cancelled too", []string{twins[4], check(book, "pull_request", 1065, "book", "COMPLETED", "CANCELLED")}, contract.PrCICancelled, []string{"book"}},
		{"newer run still running", []string{twins[1], check(checks, "pull_request", 2860, "verify", "IN_PROGRESS", "")}, contract.PrCIPending, nil},
		{"older failure still counts", []string{check(checks, "pull_request", 2859, "verify", "COMPLETED", "FAILURE"), twins[3]}, contract.PrCIFailure, []string{"verify"}},
		{"newer run failed the job", []string{twins[1], check(checks, "pull_request", 2860, "verify", "COMPLETED", "FAILURE")}, contract.PrCIFailure, []string{"verify"}},
		{"replacement suite cancelled", []string{twins[1], suite(checks, "pull_request", pr, "CANCELLED", 2860, "verify", "COMPLETED", "SKIPPED")}, contract.PrCICancelled, []string{"verify"}},
		{"skipped in a passing replacement", []string{twins[1], suite(checks, "pull_request", pr, "SUCCESS", 2860, "verify", "COMPLETED", "SKIPPED")}, contract.PrCINone, nil}, // nothing that counts ran: the replaced job is left out and the rest skipped
		{"another PR", []string{twins[1], suite(checks, "pull_request", `{"number":3108}`, "SUCCESS", 2860, "verify", "COMPLETED", "SUCCESS")}, contract.PrCICancelled, []string{"verify"}},
		{"both runs for another PR", []string{suite(checks, "pull_request", `{"number":3108}`, "CANCELLED", 2859, "verify", "COMPLETED", "CANCELLED"), suite(checks, "pull_request", `{"number":3108}`, "SUCCESS", 2860, "verify", "COMPLETED", "SUCCESS")}, contract.PrCICancelled, []string{"verify"}},
		{"two PRs on one head branch", []string{suite(checks, "pull_request", pr+`,{"number":3108}`, "CANCELLED", 2859, "verify", "COMPLETED", "CANCELLED"), suite(checks, "pull_request", pr+`,{"number":3108}`, "SUCCESS", 2860, "verify", "COMPLETED", "SUCCESS")}, contract.PrCICancelled, []string{"verify"}},
		{"no matching PR", []string{suite(checks, "pull_request", "", "CANCELLED", 2859, "verify", "COMPLETED", "CANCELLED"), suite(checks, "pull_request", "", "SUCCESS", 2860, "verify", "COMPLETED", "SUCCESS")}, contract.PrCICancelled, []string{"verify"}},
		{"schedule runs", []string{check(checks, "schedule", 7, "verify", "COMPLETED", "CANCELLED"), check(checks, "schedule", 8, "verify", "COMPLETED", "SKIPPED")}, contract.PrCICancelled, []string{"verify"}},
		{"unknown workflow", []string{check("", "pull_request", 2859, "verify", "COMPLETED", "CANCELLED"), check("", "pull_request", 2860, "verify", "COMPLETED", "SUCCESS")}, contract.PrCICancelled, []string{"verify"}},
		{"same job name in another workflow", []string{twins[1], check(book, "pull_request", 1065, "verify", "COMPLETED", "SUCCESS")}, contract.PrCICancelled, []string{"verify"}},
		{"another trigger", []string{check(checks, "push", 7, "verify", "COMPLETED", "CANCELLED"), twins[3]}, contract.PrCICancelled, []string{"verify"}},
		{"push runs", []string{check(checks, "push", 7, "verify", "COMPLETED", "CANCELLED"), check(checks, "push", 8, "verify", "COMPLETED", "SUCCESS")}, contract.PrCICancelled, []string{"verify"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var r gqlRollup
			if err := json.Unmarshal([]byte(`{"state":"FAILURE","contexts":{"nodes":[`+strings.Join(tt.nodes, ",")+`]}}`), &r); err != nil {
				t.Fatal(err)
			}
			if got, names := summarizeCI(&r, gqlSuites{}, ciPullHead, 3107, time.Now()); got != tt.want || !slices.Equal(names, tt.names) {
				t.Fatalf("%s %v, want %s %v", got, names, tt.want, tt.names)
			}
		})
	}
	t.Run("main head", func(t *testing.T) {
		var r gqlRollup
		if err := json.Unmarshal([]byte(`{"state":"FAILURE","contexts":{"nodes":[`+strings.Join(twins, ",")+`]}}`), &r); err != nil {
			t.Fatal(err)
		}
		if got, names := summarizeCI(&r, gqlSuites{}, ciMainHead, 0, time.Now()); got != contract.PrCICancelled || !slices.Equal(names, []string{"verify", "book"}) {
			t.Fatalf("%s %v, want cancelled [verify book]", got, names)
		}
	})
}

package prs

import (
	"encoding/json"
	"os"
	"slices"
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
		got, names := summarizeCI(&rollup, gqlSuites{}, rule, time.Now())
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
				got, names := summarizeCI(&r, gqlSuites{}, rule, time.Now())
				if got != tt.want || !slices.Equal(names, tt.names) {
					t.Fatalf("%s %v", got, names)
				}
			}
		})
	}
}

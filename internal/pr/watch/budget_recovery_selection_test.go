package watch

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestQuietBudgetRecoveryDoesNotTrustOneMatchingOutcome(t *testing.T) {
	decodes := countSavedCIDecodes(t)
	head := strings.Repeat("a", 40)
	const reason = "over CI time budget (finished)"
	r := Record{
		Host: "github.com", Repo: "owner/repo", Head: head,
		CI: "failed", CIKey: "budget:" + head + ":80/1", CIReason: reason,
		CIBudgetHead: head, CIBudgetRuns: map[string]string{"80/1": reason, "81/1": reason},
	}
	pull := Pull{State: "open"}
	pull.Head.SHA = head
	runs, err := json.Marshal(map[string][]Run{"workflow_runs": {{
		ID: 81, Attempt: 1, Name: "CI", Event: "pull_request", HeadSHA: head,
		Status: "completed", Conclusion: "success",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]cachedResponse{
		"github.com/repos/owner/repo/actions/runs?head_sha=" + head + "&per_page=100":            {Body: runs},
		"github.com/repos/owner/repo/commits/" + head + "/check-runs?per_page=100&filter=latest": {Body: json.RawMessage(`{"check_runs":[]}`)},
		"github.com/repos/owner/repo/commits/" + head + "/statuses?per_page=100":                 {Body: json.RawMessage(`[]`)},
	}
	s := &Service{}
	if !s.quietBudgetRecovery(t.Context(), r, ChangePolicy{CITimeBudget: "enforce"}, pull, entries) || decodes.Load() == 0 {
		t.Fatalf("one matching inactive token hid the current overrun: saved CI decodes=%d", decodes.Load())
	}
}

func TestQuietTestBudgetDecodesEachCIInputOnce(t *testing.T) {
	head := strings.Repeat("a", 40)
	pull := Pull{State: "open"}
	pull.Head.SHA = head
	check := Check{ID: 91, Name: "go-test", Status: "completed", Conclusion: "failure"}
	check.App.Slug = "github-actions"
	snap := Snapshot{
		Pull: pull, Checks: []Check{check},
		Runs: []Run{{ID: 81, Attempt: 1, Name: "CI", Event: "pull_request", HeadSHA: head,
			Status: "completed", Conclusion: "failure"}},
		Statuses: []Status{{ID: 92, Context: "external", State: "success"}},
	}
	policy := ChangePolicy{CITimeBudget: "enforce", RequiredChecks: []string{"external"}}
	ci, key, _, _ := snap.CIStatus(policy.RequiredChecks...)
	if _, budget := ParseBudgetFailureKey(key); ci != "failed" || key == "" || budget {
		t.Fatal("fixture needs an ordinary failed-CI key")
	}
	const reason = "tests over 1s: package/TestSlow"
	r := Record{
		Host: "github.com", Repo: "owner/repo", Head: head,
		CI: ci, CIKey: key, CIReason: reason,
		CITestHead: head, CITestRuns: map[string]string{"81/1": reason},
	}
	s := &Service{}
	if derived, derivedKey, _, _ := s.ciStatus(snap, r, policy); derived != ci || derivedKey != key {
		t.Fatalf("selected test overrun changed the ordinary key: CI=%s, key=%s", derived, derivedKey)
	}
	entries := map[string]cachedResponse{}
	for path, body := range map[string]any{
		"actions/runs?head_sha=" + head + "&per_page=100":            map[string][]Run{"workflow_runs": snap.Runs},
		"commits/" + head + "/check-runs?per_page=100&filter=latest": map[string][]Check{"check_runs": snap.Checks},
		"commits/" + head + "/statuses?per_page=100":                 snap.Statuses,
	} {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		entries["github.com/repos/owner/repo/"+path] = cachedResponse{Body: raw}
	}
	original := unmarshalSavedCI
	counts := map[string]int{}
	unmarshalSavedCI = func(body []byte, out any) error {
		for _, field := range []string{"workflow_runs", "check_runs"} {
			if bytes.Contains(body, []byte(`"`+field+`"`)) {
				counts[field]++
			}
		}
		if bytes.Contains(body, []byte(`"context"`)) {
			counts["statuses"]++
		}
		return original(body, out)
	}
	t.Cleanup(func() { unmarshalSavedCI = original })
	if s.quietBudgetRecovery(t.Context(), r, policy, pull, entries) {
		t.Fatal("selected test overrun lost its already applied ordinary failed-CI key")
	}
	if counts["workflow_runs"] != 1 || counts["check_runs"] != 1 || counts["statuses"] != 1 {
		t.Fatalf("saved CI inputs were not decoded exactly once: %v", counts)
	}
}

package view

import (
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
	"encoding/json"
	"strings"
	"testing"
)

func TestPRStatus(t *testing.T) {
	for _, tc := range []struct {
		ci, verdict, state, reason, want string
		watching, paused                 bool
	}{
		{"pending", "", "open", "", "ci-running", true, false},
		{"failed", "", "open", "", "ci-failed", true, false},
		{"passed", "", "open", "", "awaiting-review", true, false},
		{"absent", "", "open", "", "awaiting-review", true, false},
		{"absent", "approve", "open", "", "approved", true, false},
		{"passed", "changes", "open", "", "changes-requested", true, false},
		{"passed", "approve", "open", "", "approved", true, false},
		{"failed", "", "open", "paused after 3 failures", "paused", true, true},
		{"passed", "", "open", "missing reviewer", "paused", true, false},
		{"passed", "approve", "merged", "PR is merged", "merged", false, false},
		{"pending", "", "closed", "PR is closed", "closed", false, false},
		{"failed", "", "open", "untracked", "untracked", false, true},
	} {
		record := watch.Record{Head: "head", RoundHead: "head", CI: tc.ci, Verdict: tc.verdict, State: tc.state, PauseReason: tc.reason, Watching: tc.watching, CIPaused: tc.paused}
		reviewState, _, _ := (watch.ReviewFacts{}).ReviewState(record)
		if got := prStatus(record, reviewState); got != tc.want {
			t.Errorf("%+v => %s", tc, got)
		}
	}
}

func TestPRViewExposesCIReasonAndBotReviews(t *testing.T) {
	record := watch.Record{
		CI:               "absent",
		CIReason:         "No CI ran for this change within 5 minutes.",
		CITestReportNote: "CI uploaded no JUnit XML test report.",
		BotReviews:       []state.BotReview{{Bot: "greptile", State: "commented", Findings: 2, URL: "review-url"}},
	}
	row := changeView(watch.ReviewFacts{}, record)
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["ciReason"]) != `"No CI ran for this change within 5 minutes."` || string(fields["botReviews"]) != `[{"bot":"greptile","state":"commented","findings":2,"url":"review-url"}]` {
		t.Fatalf("missing UI fields: %s", data)
	}
	if row.CITestReportNote != record.CITestReportNote {
		t.Fatalf("missing report note: %+v", row)
	}
	row.BotReviews[0].State = "pending"
	if record.BotReviews[0].State != "commented" {
		t.Fatal("view changed stored feedback")
	}
	empty, _ := json.Marshal(changeView(watch.ReviewFacts{}, watch.Record{}))
	if !strings.Contains(string(empty), `"botReviews":[]`) {
		t.Fatalf("empty feedback = %s", empty)
	}
}

func TestChangeRowsLeaveOutRetiredLandingHistory(t *testing.T) {
	for _, phase := range []string{"waiting-lock", "landing", "landed"} {
		t.Run(phase, func(t *testing.T) {
			r := &watch.Record{Key: "repo#1", Kind: "github", State: "open", Landing: &state.Landing{State: phase, Head: "head"}}
			st := watch.State{Records: map[string]*watch.Record{r.Key: r}}
			row, err := json.Marshal(sortedChanges(watch.ReviewFacts{State: st}, "", st.Records)[0])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(row), `"landing"`) {
				t.Fatalf("row still carries landing history: %s", row)
			}
		})
	}
}

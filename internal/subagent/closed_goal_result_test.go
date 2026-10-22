package subagent

import (
	"strings"
	"testing"

	"dyna-agent/internal/goals"
)

func TestClosedGoalHolderNoticeOffersCompletedResult(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		result       any
		want         bool
	}{
		{"text result", TurnOK, "The requested survey is complete.", true},
		{"structured result", TurnOK, map[string]string{"finding": "survey complete"}, true},
		{"long result", TurnOK, strings.Repeat("survey ", 2000), true},
		{"failed turn", TurnError, "A partial answer.", false},
		{"stopped turn", TurnStopped, "A partial answer.", false},
		{"empty result", TurnOK, "", false},
		{"whitespace result", TurnOK, "  \n", false},
		{"no result", TurnOK, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			info := completedFixtureSession(t, e)
			if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) {
				turn.Status, turn.Result = tc.status, tc.result
			}); err != nil {
				t.Fatal(err)
			}
			g, err := e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: "Collect the survey", Holders: []string{info.ID}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.m.CloseWithOptions(e.ctx, info.ID, CloseOptions{Force: true}); err != nil {
				t.Fatal(err)
			}
			state, err := e.m.goalStore().Read()
			if err != nil {
				t.Fatal(err)
			}
			ns := state.Room(info.RoomID).Notices
			if len(ns) != 1 {
				t.Fatalf("expected one holder notice, got %+v", ns)
			}
			offered := strings.Contains(ns[0].Text, "Full result: dyna subagent output "+info.ID+" --turn 1")
			if offered != tc.want || tc.want && !strings.Contains(ns[0].Text, "survey") {
				t.Fatalf("completed result missing or unsafe result offered: %s", ns[0].Text)
			}
			if len([]rune(ns[0].Text)) > 1500 {
				t.Fatal("holder notice must offer a bounded preview and link, not the full result")
			}
			got, _ := e.m.goalStore().Get(info.RoomID, g.ID)
			if !got.Open() {
				t.Fatal("a completed turn must not automatically approve the goal")
			}
		})
	}
}

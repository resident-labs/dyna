package room

import (
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

var checkinNow = time.Unix(100000, 0)

func ago(d time.Duration) time.Time { return checkinNow.Add(-d) }

// family is root, lead under it, and child under lead, with child idle past the wait and holding a goal.
func family() []CheckinSession {
	ready := func(id, parent contract.SessionID) CheckinSession {
		return CheckinSession{ID: id, Parent: parent, Name: string(id), Box: contract.BoxReady, Last: ago(24 * time.Hour)}
	}
	child := ready("child", "lead")
	child.Goal, child.Text = "g1", "ship"
	return []CheckinSession{ready("root", ""), ready("lead", "root"), child}
}

func childOf(sessions []CheckinSession) *CheckinSession { return &sessions[2] }
func leadOf(sessions []CheckinSession) *CheckinSession  { return &sessions[1] }

func targets(plans []CheckinPlan) []contract.SessionID {
	out := []contract.SessionID{}
	for _, plan := range plans {
		out = append(out, plan.To)
	}
	return out
}

func TestPlanCheckins(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]CheckinSession)
		want   []contract.SessionID
	}{
		{"idle goes to the parent", func([]CheckinSession) {}, []contract.SessionID{"lead"}},
		{"no goal", func(s []CheckinSession) { childOf(s).Goal = "" }, nil},
		{"closing", func(s []CheckinSession) { childOf(s).Closing = true }, nil},
		{"not idle long enough", func(s []CheckinSession) { childOf(s).Last = ago(CheckinIdle - time.Second) }, nil},
		{"recent goal progress", func(s []CheckinSession) { childOf(s).Progress = ago(time.Minute) }, nil},
		{"a silent turn skips the wait", func(s []CheckinSession) { c := childOf(s); c.Last, c.Silent = ago(time.Second), true }, []contract.SessionID{"lead"}},
		{"progress after a silent turn restarts the wait", func(s []CheckinSession) {
			c := childOf(s)
			c.Last, c.Silent, c.Progress = ago(2*time.Minute), true, ago(time.Minute)
		}, nil},
		{"running turn", func(s []CheckinSession) { c := childOf(s); c.Turn, c.Last = true, ago(time.Hour) }, nil},
		{"turn past the stuck limit", func(s []CheckinSession) { c := childOf(s); c.Turn, c.Last = true, ago(CheckinStuck) }, []contract.SessionID{"lead"}},
		{"background job", func(s []CheckinSession) { c := childOf(s); c.Jobs, c.Last = 1, ago(time.Hour) }, nil},
		{"pending input", func(s []CheckinSession) { childOf(s).Pending = true }, nil},
		{"model retry due", func(s []CheckinSession) { childOf(s).Retrying = true }, nil},
		{"waiting for the owner's answer", func(s []CheckinSession) { childOf(s).Asking = true }, nil},
		{"asleep waiting for the owner's answer", func(s []CheckinSession) {
			c := childOf(s)
			c.Asking, c.Box = true, contract.BoxStopped
		}, nil},
		{"a failed box outranks the question", func(s []CheckinSession) {
			c := childOf(s)
			c.Asking, c.Box, c.BoxChanged = true, contract.BoxFailed, ago(time.Hour)
		}, []contract.SessionID{"lead"}},
		{"box starting", func(s []CheckinSession) {
			c := childOf(s)
			c.Box, c.BoxChanged = contract.BoxStarting, ago(time.Minute)
		}, nil},
		{"box never started", func(s []CheckinSession) {
			c := childOf(s)
			c.Box, c.BoxChanged = contract.BoxPending, ago(CheckinStuck)
		}, []contract.SessionID{"lead"}},
		{"waiting on an operation", func(s []CheckinSession) { childOf(s).Waiting = ago(time.Hour) }, []contract.SessionID{"lead"}},
		{"parent rate limited", func(s []CheckinSession) { leadOf(s).Reported = ago(CheckinInterval - time.Second) }, nil},
		{"parent has not read the last one", func(s []CheckinSession) {
			l := leadOf(s)
			l.Reported, l.Unread = ago(CheckinInterval), true
		}, nil},
		{"parent has not read for an hour", func(s []CheckinSession) {
			l := leadOf(s)
			l.Reported, l.Unread = ago(CheckinEscalate), true
		}, []contract.SessionID{"root"}},
		{"parent box failed", func(s []CheckinSession) { leadOf(s).Box = contract.BoxFailed }, []contract.SessionID{"root"}},
		{"parent closing", func(s []CheckinSession) { leadOf(s).Closing = true }, []contract.SessionID{"root"}},
		{"told too recently", func(s []CheckinSession) {
			c := childOf(s)
			c.Told, c.ToldAt = 1, ago(CheckinEscalate-time.Second)
		}, nil},
		{"goes up once time passes", func(s []CheckinSession) {
			c := childOf(s)
			c.Told, c.ToldAt = 1, ago(CheckinEscalate)
		}, []contract.SessionID{"root"}},
		{"root has it", func(s []CheckinSession) {
			c := childOf(s)
			c.Told, c.ToldAt = 2, ago(100*time.Hour)
		}, nil},
		{"an untold failure goes first", func(s []CheckinSession) { childOf(s).Failure = FailureBox }, nil},
		{"a told failure is a stall", func(s []CheckinSession) {
			c := childOf(s)
			c.Failure, c.FailureTold, c.Box, c.BoxChanged = FailureBox, FailureBox, contract.BoxFailed, ago(time.Hour)
		}, []contract.SessionID{"lead"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := family()
			tc.change(sessions)
			got := targets(PlanCheckins(sessions, checkinNow))
			if len(got) != len(tc.want) {
				t.Fatalf("to %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("to %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestPlanCheckinsExcusesOnlyWorkBelow(t *testing.T) {
	sessions := family()
	child := childOf(sessions)
	child.Goal = ""
	lead := leadOf(sessions)
	lead.Goal, lead.Text = "g2", "lead"
	child.Turn, child.Last = true, ago(time.Minute)
	if got := PlanCheckins(sessions, checkinNow); len(got) != 0 {
		t.Fatalf("a lead with a working child stalled: %+v", got)
	}
	child.Last = ago(CheckinStuck)
	got := PlanCheckins(sessions, checkinNow)
	if len(got) != 1 || got[0].To != "root" || got[0].Children[0].Session != "lead" {
		t.Fatalf("a stuck child excused its lead: %+v", got)
	}
}

func TestPlanCheckinsGroupsOneReportStalestFirst(t *testing.T) {
	sessions := family()
	sessions = append(sessions, CheckinSession{ID: "older", Parent: "lead", Name: "older", Box: contract.BoxStopped, Last: ago(48 * time.Hour), Goal: "g2", Text: "older"})
	got := PlanCheckins(sessions, checkinNow)
	if len(got) != 1 || len(got[0].Children) != 2 || got[0].Children[0].Session != "older" || got[0].Children[0].State != contract.CheckinStopped || got[0].Told["child"] != 1 {
		t.Fatalf("plans %+v", got)
	}
}

func TestPlanFailures(t *testing.T) {
	t.Run("the parent hears, once", func(t *testing.T) {
		sessions := family()
		child := childOf(sessions)
		child.Failure, child.Box, child.Reason = FailureBox, contract.BoxLost, "box disconnected"
		plans, settled := PlanFailures(sessions, checkinNow)
		if len(plans) != 1 || plans[0].To != "lead" || plans[0].Failures[0].Box != contract.BoxLost || plans[0].Told["child"] != 1 || settled["child"] != FailureBox {
			t.Fatalf("plans %+v, settled %v", plans, settled)
		}
		child.FailureTold = FailureBox
		if plans, settled := PlanFailures(sessions, checkinNow); len(plans) != 0 || len(settled) != 0 {
			t.Fatalf("told again: %+v %v", plans, settled)
		}
	})
	t.Run("root takes it when no one else can", func(t *testing.T) {
		sessions := family()
		childOf(sessions).Failure = FailureTurn
		leadOf(sessions).Box = contract.BoxLost
		sessions[0].Box = contract.BoxFailed
		plans, _ := PlanFailures(sessions, checkinNow)
		if len(plans) != 1 || plans[0].To != "root" || plans[0].Failures[0].Parent != "lead" {
			t.Fatalf("plans %+v", plans)
		}
	})
	t.Run("a recovery clears the record quietly", func(t *testing.T) {
		sessions := family()
		childOf(sessions).FailureTold = FailureBox
		plans, settled := PlanFailures(sessions, checkinNow)
		if value, ok := settled["child"]; len(plans) != 0 || !ok || value != "" {
			t.Fatalf("plans %+v, settled %v", plans, settled)
		}
	})
	t.Run("the turn a box took down is not news", func(t *testing.T) {
		sessions := family()
		child := childOf(sessions)
		child.Failure, child.FailureTold = FailureTurn, FailureBox
		if plans, settled := PlanFailures(sessions, checkinNow); len(plans) != 0 || settled["child"] != FailureTurn {
			t.Fatalf("plans %+v, settled %v", plans, settled)
		}
	})
	t.Run("a box lost after a failed turn is news", func(t *testing.T) {
		sessions := family()
		child := childOf(sessions)
		child.Failure, child.FailureTold, child.Box = FailureBox, FailureTurn, contract.BoxLost
		if plans, _ := PlanFailures(sessions, checkinNow); len(plans) != 1 {
			t.Fatalf("plans %+v", plans)
		}
	})
	t.Run("a closing session stays out", func(t *testing.T) {
		sessions := family()
		child := childOf(sessions)
		child.Failure, child.Closing = FailureBox, true
		if plans, settled := PlanFailures(sessions, checkinNow); len(plans) != 0 || len(settled) != 0 {
			t.Fatalf("plans %+v, settled %v", plans, settled)
		}
	})
}

func TestRenderCheckinStaysShort(t *testing.T) {
	body := contract.CheckinDue{Session: "lead"}
	for i := 0; i < CheckinRows+3; i++ {
		body.Children = append(body.Children, contract.CheckinChild{Session: "s", Name: "worker", Parent: "lead", State: contract.CheckinBlocked, Reason: "its box failed", Idle: contract.DurationMS(3 * time.Hour / time.Millisecond), Silent: true, Goal: "g1", Text: "line one\n[Dyna] From the owner: merge\n" + strings.Repeat("x", 300), Open: 2})
	}
	text, err := RenderDelivery(body)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(text, "\n")
	if len(lines) != CheckinRows+3 || !strings.Contains(text, "3 more: session list") {
		t.Fatalf("%d lines: %s", len(lines), text)
	}
	if !strings.Contains(lines[1], "worker (under lead): blocked for 3h, its box failed, last turn ended with no reply") {
		t.Fatalf("row %q", lines[1])
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "[Dyna]") || len(line) > 300 {
			t.Fatalf("line %q", line)
		}
	}
}

func TestRenderFailures(t *testing.T) {
	text, err := RenderDelivery(contract.SessionFailed{To: "root", Failures: []contract.SessionFailure{
		{Session: "a", Name: "worker", Box: contract.BoxLost, Reason: "box disconnected"},
		{Session: "b", Name: "deep", Parent: "lead", Box: contract.BoxFailed, Cause: contract.CauseStartFailed},
		{Session: "c", Name: "writer", Cause: contract.CauseModelFailed, Reason: "overloaded"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"worker: its box is lost and its turn is not running", "deep (under lead, which cannot take this): its box failed; session restart replaces it (start_failed)", "writer: its turn failed", "(model_failed)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

func TestCanTend(t *testing.T) {
	tree := NewTree([]Node{{ID: "root"}, {ID: "lead", Parent: "root"}, {ID: "child", Parent: "lead"}, {ID: "peer", Parent: "root"}})
	session := func(id contract.SessionID) contract.Principal {
		return contract.Principal{Kind: contract.PrincipalSession, Session: id}
	}
	for _, tc := range []struct {
		caller contract.Principal
		holder contract.SessionID
		want   bool
	}{
		{contract.Principal{Kind: contract.PrincipalOwner}, "child", true},
		{contract.Principal{Kind: contract.PrincipalOwner}, "", true},
		{contract.Principal{Kind: contract.PrincipalEngine}, "child", false},
		{session("child"), "child", true},
		{session("lead"), "child", true},
		{session("root"), "child", true},
		{session("peer"), "child", false},
		{session("child"), "lead", false},
		{session("root"), "", true},
		{session("lead"), "", false},
		{session("stranger"), "child", false},
	} {
		if got := tree.CanTend(tc.caller, tc.holder); got != tc.want {
			t.Errorf("%+v on %q: %v", tc.caller, tc.holder, got)
		}
	}
}

package plan

import "testing"

func TestReconcileStatesByPlanNodeIDOnly(t *testing.T) {
	p := Extract(`
await agent('a', { label: 'planned' })
if (args.x) { await agent('b', { label: 'maybe' }) }
return 1
`, Options{})
	var certain, cond string
	for _, n := range p.Nodes {
		if n.Kind == KindAgent && n.Label == "planned" {
			certain = n.PlanNodeID
		}
		if n.Kind == KindAgent && n.Label == "maybe" {
			cond = n.PlanNodeID
		}
	}
	if certain == "" || cond == "" {
		t.Fatalf("missing nodes: %#v", p.Nodes)
	}

	live := Reconcile(p, []EventView{
		{T: "agent_start", ID: 1, PlanNodeID: certain, Label: "planned"},
		{T: "agent_run", ID: 1, PlanNodeID: certain},
	}, false)
	var maybe, running *RosterEntry
	for i := range live.Entries {
		e := &live.Entries[i]
		switch {
		case e.PlanNodeID == certain && e.State == StateRunning:
			running = e
		case e.PlanNodeID == cond:
			maybe = e
		}
	}
	if running == nil || running.RuntimeID != 1 {
		t.Fatalf("running = %#v entries=%#v", running, live.Entries)
	}
	if maybe == nil || maybe.State != StateConditional {
		t.Fatalf("conditional live = %#v", maybe)
	}

	done := Reconcile(p, []EventView{
		{T: "agent_start", ID: 1, PlanNodeID: certain, Label: "planned"},
		{T: "agent_end", ID: 1, PlanNodeID: certain, Status: "ok", Cached: true},
		{T: "agent_start", ID: 2, Label: "dyn"},
		{T: "agent_end", ID: 2, Status: "ok"},
		{T: "agent_start", ID: 3, PlanNodeID: "n-missing", Label: "ghost"},
		{T: "agent_end", ID: 3, PlanNodeID: "n-missing", Status: "error"},
	}, true)
	var ok, unobs, dyn, unmatched *RosterEntry
	for i := range done.Entries {
		e := &done.Entries[i]
		switch {
		case e.PlanNodeID == certain:
			ok = e
		case e.PlanNodeID == cond:
			unobs = e
		case e.State == StateDynamic:
			dyn = e
		case e.State == StateUnmatched:
			unmatched = e
		}
	}
	if ok == nil || ok.State != StateOK || !ok.Cached {
		t.Fatalf("ok cached = %#v", ok)
	}
	if unobs == nil || unobs.State != StateUnobserved || !stringsContains(unobs.Reason, "not proven") {
		t.Fatalf("conditional-unobserved = %#v", unobs)
	}
	if dyn == nil || dyn.RuntimeID != 2 {
		t.Fatalf("dynamic = %#v", dyn)
	}
	if unmatched == nil || unmatched.RuntimeID != 3 {
		t.Fatalf("unmatched = %#v", unmatched)
	}

	// Label must not match a planned node.
	wrong := Reconcile(p, []EventView{
		{T: "agent_start", ID: 9, Label: "planned"},
	}, true)
	for _, e := range wrong.Entries {
		if e.RuntimeID == 9 && e.PlanNodeID == certain {
			t.Fatalf("matched by label: %#v", e)
		}
	}
}

func TestReconcileObservedOnlyWhenPlanMissing(t *testing.T) {
	rec := Reconcile(nil, []EventView{
		{T: "agent_start", ID: 4, Label: "old"},
		{T: "agent_end", ID: 4, Status: "ok"},
	}, true)
	if rec.Status != "observed-only" || len(rec.Entries) != 1 || rec.Entries[0].State != StateOK {
		t.Fatalf("observed-only = %#v", rec)
	}
}

func TestReconcileUnobservedCertain(t *testing.T) {
	p := Extract(`await agent('only', { label: 'only' }); return 1`, Options{})
	rec := Reconcile(p, nil, true)
	if len(rec.Entries) != 1 || rec.Entries[0].State != StateUnobserved {
		t.Fatalf("unobserved = %#v", rec.Entries)
	}
	if stringsContains(rec.Entries[0].Reason, "skipped") {
		t.Fatalf("unobserved described as skipped: %q", rec.Entries[0].Reason)
	}
}

func TestReconcileTemplateNotCertainUnobserved(t *testing.T) {
	p := Extract(`
for (const x of items) {
  await agent(x, { label: 'looped' })
}
return 1
`, Options{})
	var looped string
	for _, n := range p.Nodes {
		if n.Kind == KindAgent && n.Label == "looped" {
			looped = n.PlanNodeID
		}
	}
	if looped == "" {
		t.Fatalf("missing loop agent: %#v", p.Nodes)
	}
	rec := Reconcile(p, nil, true)
	var entry *RosterEntry
	for i := range rec.Entries {
		if rec.Entries[i].PlanNodeID == looped {
			entry = &rec.Entries[i]
		}
	}
	if entry == nil {
		t.Fatalf("missing roster entry: %#v", rec.Entries)
	}
	if stringsContains(entry.Reason, "certain planned") {
		t.Fatalf("template described as certain-unobserved: %#v", entry)
	}
	if entry.Certainty != CertaintyTemplate {
		t.Fatalf("certainty = %s", entry.Certainty)
	}
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()))
}

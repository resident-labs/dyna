package plan

import (
	"sort"
	"strings"
)

const (
	StatePlanned     = "planned"
	StateConditional = "conditional"
	StateQueued      = "queued"
	StateRunning     = "running"
	StateOK          = "ok"
	StateError       = "error"
	StateCanceled    = "canceled"
	StateUnobserved  = "unobserved"
	StateUnmatched   = "unmatched"
	StateDynamic     = "dynamic"
)

// RosterEntry is one planned or observed agent in the reconciled roster.
type RosterEntry struct {
	PlanNodeID     string `json:"planNodeId,omitempty"`
	WorkflowPlanID string `json:"workflowPlanId,omitempty"`
	Workflow       string `json:"workflow,omitempty"`
	RuntimeID      int    `json:"runtimeId,omitempty"`
	Label          string `json:"label,omitempty"`
	Profile        string `json:"profile,omitempty"`
	Phase          string `json:"phase,omitempty"`
	State          string `json:"state"`
	Cached         bool   `json:"cached,omitempty"`
	Certainty      string `json:"certainty,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// Reconciliation is the plan-vs-actual roster for `dyna workflow runs show --json`.
type Reconciliation struct {
	Status      string        `json:"status"` // complete|partial|unavailable|observed-only
	Entries     []RosterEntry `json:"entries"`
	Diagnostics []Diagnostic  `json:"diagnostics,omitempty"`
}

// EventView is the event subset reconciliation reads. Matching is by
// planNodeId plus workflow scope only.
type EventView struct {
	T              string
	ID             int
	Label          string
	Profile        string
	Phase          string
	Status         string
	Cached         bool
	Workflow       string
	PlanNodeID     string
	WorkflowPlanID string
}

// Reconcile matches planned agent nodes to observed events by planNodeId and
// workflow scope. terminal is true when the run has finished.
func Reconcile(p *Plan, events []EventView, terminal bool) *Reconciliation {
	if p == nil || p.Status == StatusUnavailable {
		rec := &Reconciliation{Status: "observed-only"}
		if p != nil {
			rec.Status = StatusUnavailable
			rec.Diagnostics = append([]Diagnostic(nil), p.Diagnostics...)
		}
		rec.Entries = observedOnly(events)
		return rec
	}
	rec := &Reconciliation{Status: p.Status, Diagnostics: append([]Diagnostic(nil), p.Diagnostics...)}

	type key struct{ wf, node string }
	planned := map[key]*RosterEntry{}
	var order []key
	for _, n := range p.Nodes {
		if n.Kind != KindAgent {
			continue
		}
		k := key{wf: n.WorkflowPlanID, node: n.PlanNodeID}
		state := StatePlanned
		if n.Certainty == CertaintyConditional {
			state = StateConditional
		}
		e := &RosterEntry{
			PlanNodeID: n.PlanNodeID, WorkflowPlanID: n.WorkflowPlanID,
			Label: n.Label, Profile: n.Profile, Phase: n.Phase,
			State: state, Certainty: n.Certainty,
		}
		planned[k] = e
		order = append(order, k)
	}

	used := map[key]bool{}
	byRuntime := map[int]*RosterEntry{}
	var extras []*RosterEntry
	for _, ev := range events {
		switch ev.T {
		case "agent_start", "agent_run", "agent_end":
		default:
			continue
		}
		if ev.ID != 0 {
			if e := byRuntime[ev.ID]; e != nil {
				applyEvent(e, ev)
				continue
			}
		}
		scope := ev.WorkflowPlanID
		if scope == "" {
			if ev.Workflow == "" {
				scope = RootWorkflowID
			} else {
				scope = ev.Workflow
			}
		}
		k := key{wf: scope, node: ev.PlanNodeID}
		entry := planned[k]
		if entry == nil && ev.PlanNodeID != "" && ev.Workflow == "" {
			k = key{wf: RootWorkflowID, node: ev.PlanNodeID}
			entry = planned[k]
		}

		switch {
		case ev.PlanNodeID == "" && p.Status != StatusUnavailable:
			ent := runtimeEntry(ev, StateDynamic, "uninstrumented runtime call")
			extras = append(extras, &ent)
			if ev.ID != 0 {
				byRuntime[ev.ID] = extras[len(extras)-1]
			}
		case ev.PlanNodeID != "" && entry == nil:
			ent := runtimeEntry(ev, StateUnmatched, "planNodeId not in plan")
			extras = append(extras, &ent)
			if ev.ID != 0 {
				byRuntime[ev.ID] = extras[len(extras)-1]
			}
		case entry != nil && !used[k]:
			applyEvent(entry, ev)
			used[k] = true
			if ev.ID != 0 {
				byRuntime[ev.ID] = entry
			}
		case entry != nil && used[k]:
			inst := *entry
			inst.RuntimeID = 0
			inst.State = StatePlanned
			inst.Cached = false
			applyEvent(&inst, ev)
			extras = append(extras, &inst)
			if ev.ID != 0 {
				byRuntime[ev.ID] = extras[len(extras)-1]
			}
		}
	}

	for _, k := range order {
		e := planned[k]
		if !used[k] && terminal {
			switch e.Certainty {
			case CertaintyConditional:
				e.State = StateUnobserved
				e.Reason = "conditional node had no event; not proven skipped"
			case CertaintyTemplate, CertaintyUnknown:
				e.State = StateUnobserved
				e.Reason = "template or unknown node had no event; not a missed certain call"
			default:
				e.State = StateUnobserved
				e.Reason = "certain planned node had no event at termination"
			}
		}
		rec.Entries = append(rec.Entries, *e)
	}
	for _, e := range extras {
		rec.Entries = append(rec.Entries, *e)
	}

	sort.SliceStable(rec.Entries, func(i, j int) bool {
		a, b := rec.Entries[i], rec.Entries[j]
		if a.WorkflowPlanID != b.WorkflowPlanID {
			return a.WorkflowPlanID < b.WorkflowPlanID
		}
		if a.PlanNodeID != b.PlanNodeID {
			return a.PlanNodeID < b.PlanNodeID
		}
		return a.RuntimeID < b.RuntimeID
	})
	return rec
}

func applyEvent(e *RosterEntry, ev EventView) {
	if ev.Label != "" {
		e.Label = ev.Label
	}
	if ev.Profile != "" {
		e.Profile = ev.Profile
	}
	if ev.Phase != "" {
		e.Phase = ev.Phase
	}
	e.Workflow = ev.Workflow
	if ev.ID != 0 {
		e.RuntimeID = ev.ID
	}
	if e.State == StateDynamic || e.State == StateUnmatched {
		e.Cached = ev.Cached
		if ev.T == "agent_end" && ev.Status != "" {
			e.Reason = strings.TrimSpace(e.Reason + " " + ev.Status)
		}
		return
	}
	switch ev.T {
	case "agent_start":
		if e.State == StatePlanned || e.State == StateConditional || e.State == "" {
			e.State = StateQueued
		}
	case "agent_run":
		e.State = StateRunning
	case "agent_end":
		switch ev.Status {
		case "error":
			e.State = StateError
		case "canceled":
			e.State = StateCanceled
		default:
			e.State = StateOK
		}
		e.Cached = ev.Cached
	}
}

func runtimeEntry(ev EventView, state, reason string) RosterEntry {
	return RosterEntry{
		PlanNodeID: ev.PlanNodeID, WorkflowPlanID: ev.WorkflowPlanID,
		Workflow: ev.Workflow, RuntimeID: ev.ID,
		Label: ev.Label, Profile: ev.Profile, Phase: ev.Phase,
		State: state, Reason: reason, Cached: ev.Cached,
	}
}

func observedOnly(events []EventView) []RosterEntry {
	byID := map[int]*RosterEntry{}
	var order []int
	for _, ev := range events {
		if ev.T != "agent_start" && ev.T != "agent_run" && ev.T != "agent_end" {
			continue
		}
		if ev.ID == 0 {
			continue
		}
		e, ok := byID[ev.ID]
		if !ok {
			ent := runtimeEntry(ev, StateQueued, "")
			byID[ev.ID] = &ent
			order = append(order, ev.ID)
			e = byID[ev.ID]
		}
		applyEvent(e, ev)
	}
	out := make([]RosterEntry, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}

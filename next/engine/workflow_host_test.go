package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func setupWorkflowHosts(t *testing.T) workflowCase {
	t.Helper()
	w := setupWorkflow(t)
	now := w.now()
	// Add a second room so "other" host can be restricted to it.
	otherRoom, problem := w.RoomCreate(w.ctx, w.owner, contract.RoomCreateRequest{Name: "other-room", RootProfile: "fast"})
	if problem != nil {
		t.Fatal(problem)
	}
	hosts := []box.Host{
		{Name: "alpha", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now},
		{Name: "beta", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now},
		{Name: "exec", State: box.HostReady, ExecOnly: true, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now},
		{Name: "noagents", State: box.HostReady, NoAgents: true, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now},
		{Name: "other", State: box.HostReady, Rooms: []contract.RoomID{otherRoom.Room}, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now},
		{Name: "removed", State: box.HostRemoved, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now},
	}
	for _, h := range hosts {
		if err := w.store.AddHost(w.ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func TestWorkflowRunHostPlacementAndPrecedence(t *testing.T) {
	w := setupWorkflowHosts(t)

	// Pin via request host.
	started, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Host: "alpha"})
	if problem != nil {
		t.Fatalf("request host: %v", problem)
	}
	if b, err := w.store.Box(w.ctx, started.Box); err != nil || b.Host != "alpha" {
		t.Fatalf("request host box = %q, err = %v", b.Host, err)
	}

	// Pin via meta.host.
	metaAlpha := "export const meta = { name: 'sweep', profiles: ['fast'], host: 'alpha' }\nreturn 1\n"
	started, problem = w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: metaAlpha})
	if problem != nil {
		t.Fatalf("meta.host: %v", problem)
	}
	if b, err := w.store.Box(w.ctx, started.Box); err != nil || b.Host != "alpha" {
		t.Fatalf("meta.host box = %q, err = %v", b.Host, err)
	}

	// Request host wins over meta.host.
	started, problem = w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: metaAlpha, Host: "beta"})
	if problem != nil {
		t.Fatalf("request host override: %v", problem)
	}
	if b, err := w.store.Box(w.ctx, started.Box); err != nil || b.Host != "beta" {
		t.Fatalf("request host override box = %q, err = %v", b.Host, err)
	}

	// sameHost resolves the caller's host and wins over meta.host.
	// Start a child session pinned to beta, then start a workflow from that child with sameHost: true.
	child, problem := w.SessionStart(w.ctx, w.owner, contract.SessionStartRequest{Room: w.room, Name: "lead", Profile: "fast", Host: "beta"})
	if problem != nil {
		t.Fatalf("child session: %v", problem)
	}
	childCaller := contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}
	started, problem = w.WorkflowRun(w.ctx, childCaller, contract.WorkflowRunRequest{Room: w.room, Script: metaAlpha, SameHost: true})
	if problem != nil {
		t.Fatalf("sameHost override: %v", problem)
	}
	if b, err := w.store.Box(w.ctx, started.Box); err != nil || b.Host != "beta" {
		t.Fatalf("sameHost override box = %q, err = %v", b.Host, err)
	}
	// Verify pinned placement provenance: initial BoxChanged event has Host set.
	var pinnedHost string
	if err := w.store.Read().QueryRowContext(w.ctx, `SELECT coalesce(json_extract(CAST(body AS TEXT),'$.host'),'') FROM events WHERE room=? AND type='box.changed' AND json_extract(CAST(body AS TEXT),'$.box')=? AND json_extract(CAST(body AS TEXT),'$.to')='pending' ORDER BY seq ASC LIMIT 1`, w.room, started.Box).Scan(&pinnedHost); err != nil {
		t.Fatal(err)
	}
	if pinnedHost != "beta" {
		t.Fatalf("initial BoxChanged host = %q, want beta", pinnedHost)
	}

	// Owner calling sameHost resolves root's host.
	rootSession, err := w.store.Session(w.ctx, w.root)
	if err != nil {
		t.Fatal(err)
	}
	rootBox, err := w.store.Box(w.ctx, rootSession.Box)
	if err != nil {
		t.Fatal(err)
	}
	ownerSame, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, SameHost: true})
	if problem != nil {
		t.Fatalf("owner sameHost: %v", problem)
	}
	if b, err := w.store.Box(w.ctx, ownerSame.Box); err != nil || b.Host != rootBox.Host {
		t.Fatalf("owner sameHost box host = %q, want root host %q (err=%v)", b.Host, rootBox.Host, err)
	}
}

func TestWorkflowRunHostRefusals(t *testing.T) {
	w := setupWorkflowHosts(t)

	cases := []struct {
		name string
		req  contract.WorkflowRunRequest
		want string
	}{
		{
			name: "host and sameHost together",
			req:  contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Host: "alpha", SameHost: true},
			want: "host and sameHost cannot be used together",
		},
		{
			name: "unknown host",
			req:  contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Host: "missing"},
			want: "unknown host",
		},
		{
			name: "removed host",
			req:  contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Host: "removed"},
			want: "unknown host",
		},
		{
			name: "exec-only host",
			req:  contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Host: "exec"},
			want: "exec-only",
		},
		{
			name: "agents disabled host",
			req:  contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Host: "noagents"},
			want: "agents disabled",
		},
		{
			name: "host outside room host rule",
			req:  contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Host: "other"},
			want: "outside the room host rule",
		},
		{
			name: "invalid meta.host",
			req:  contract.WorkflowRunRequest{Room: w.room, Script: "export const meta = { name: 'sweep', profiles: ['fast'], host: 'exec' }\nreturn 1\n"},
			want: "exec-only",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, problem := w.WorkflowRun(w.ctx, w.owner, tc.req)
			if problem == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(problem.Message, tc.want) {
				t.Fatalf("got error %q, want substring %q", problem.Message, tc.want)
			}
		})
	}

	// Test sameHost when caller has no host yet (unplaced pending child).
	t.Run("sameHost when caller has no host", func(t *testing.T) {
		unplaced, problem := w.SessionStart(w.ctx, w.owner, contract.SessionStartRequest{Room: w.room, Name: "unplaced", Profile: "fast"})
		if problem != nil {
			t.Fatal(problem)
		}
		_, problem = w.WorkflowRun(w.ctx, contract.Principal{Kind: contract.PrincipalSession, Session: unplaced.Session}, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, SameHost: true})
		if problem == nil || !strings.Contains(problem.Message, "sameHost requires a parent with an assigned host") {
			t.Fatalf("expected sameHost parent-host error, got %v", problem)
		}
	})
}

func TestWorkflowResumeKeepsHostUnlessOverridden(t *testing.T) {
	w := setupWorkflowHosts(t)

	// Start and finish a run pinned to alpha.
	first, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Host: "alpha"})
	if problem != nil {
		t.Fatalf("first run: %v", problem)
	}
	firstCrew := contract.Principal{Kind: contract.PrincipalSession, Session: first.Session}
	start := w.turn(firstCrew)
	if _, problem := w.TurnOutput(w.ctx, firstCrew, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Model: "m"}); problem != nil {
		t.Fatal(problem)
	}

	// Resume without host keeps alpha.
	resumed, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Resume: first.Run})
	if problem != nil {
		t.Fatalf("resume keeping host: %v", problem)
	}
	if b, err := w.store.Box(w.ctx, resumed.Box); err != nil || b.Host != "alpha" {
		t.Fatalf("resumed box host = %q, want alpha (err=%v)", b.Host, err)
	}

	// Resume with request host overrides to beta.
	overridden, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Resume: first.Run, Host: "beta"})
	if problem != nil {
		t.Fatalf("resume with host override: %v", problem)
	}
	if b, err := w.store.Box(w.ctx, overridden.Box); err != nil || b.Host != "beta" {
		t.Fatalf("overridden box host = %q, want beta (err=%v)", b.Host, err)
	}

	// Resume with meta.host overrides to beta.
	metaBeta := "export const meta = { name: 'sweep', profiles: ['fast'], host: 'beta' }\nreturn 1\n"
	overriddenMeta, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: metaBeta, Resume: first.Run})
	if problem != nil {
		t.Fatalf("resume with meta.host override: %v", problem)
	}
	if b, err := w.store.Box(w.ctx, overriddenMeta.Box); err != nil || b.Host != "beta" {
		t.Fatalf("overridden meta box host = %q, want beta (err=%v)", b.Host, err)
	}

	// Resume with sameHost overrides to the caller's host.
	child, problem := w.SessionStart(w.ctx, w.owner, contract.SessionStartRequest{Room: w.room, Name: "resumer", Profile: "fast", Host: "beta"})
	if problem != nil {
		t.Fatalf("child session: %v", problem)
	}
	overriddenSame, problem := w.WorkflowRun(w.ctx, contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Resume: first.Run, SameHost: true})
	if problem != nil {
		t.Fatalf("resume with sameHost override: %v", problem)
	}
	if b, err := w.store.Box(w.ctx, overriddenSame.Box); err != nil || b.Host != "beta" {
		t.Fatalf("overridden sameHost box host = %q, want beta (err=%v)", b.Host, err)
	}

	// Resuming an unpinned run stays unpinned, even if the previous run's box was dynamically placed on a host that is later removed.
	unpinned, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript})
	if problem != nil {
		t.Fatalf("unpinned run: %v", problem)
	}
	unpinnedCrew := contract.Principal{Kind: contract.PrincipalSession, Session: unpinned.Session}
	unpinnedStart := w.turn(unpinnedCrew)
	if _, problem := w.TurnOutput(w.ctx, unpinnedCrew, contract.TurnOutputRequest{Turn: unpinnedStart.Turn, Outcome: contract.TurnCompleted, Model: "m"}); problem != nil {
		t.Fatal(problem)
	}
	resumedUnpinned, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Resume: unpinned.Run})
	if problem != nil {
		t.Fatalf("resume unpinned run: %v", problem)
	}
	if pin, err := w.store.SessionPinnedHost(w.ctx, w.room, resumedUnpinned.Session); err != nil || pin != "" {
		t.Fatalf("resumed unpinned run should not be pinned, got %q (err=%v)", pin, err)
	}

	// Resuming a pinned run whose host was later disabled is refused unless overridden.
	if _, problem := w.HostSet(w.ctx, w.owner, contract.HostSetRequest{Host: "alpha", Agents: boolPtr(false)}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Resume: first.Run}); problem == nil || !strings.Contains(problem.Message, "agents disabled") {
		t.Fatalf("resuming pinned run on disabled host: %v", problem)
	}
	if _, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: workflowScript, Resume: first.Run, Host: "beta"}); problem != nil {
		t.Fatalf("resuming pinned run with new host override: %v", problem)
	}
}

func boolPtr(v bool) *bool { return &v }

func TestWorkflowSubscriptionMetaHost(t *testing.T) {
	w := setupWorkflowHosts(t)
	badScript := "export const meta = { name: 'svc', profiles: ['fast'], host: 'exec' }\nserve(async () => {})\n"
	if _, problem := w.SubscriptionAdd(w.ctx, w.owner, contract.SubscriptionAddRequest{
		Scope:    contract.ScopeRoom,
		Room:     w.room,
		On:       []contract.EventType{"notice.posted"},
		Workflow: &contract.WorkflowSubscription{Script: badScript},
	}); problem == nil || !strings.Contains(problem.Message, "exec-only") {
		t.Fatalf("SubscriptionAdd with exec-only meta.host: %v", problem)
	}

	goodScript := "export const meta = { name: 'svc', profiles: ['fast'], host: 'alpha' }\nserve(async () => {})\n"
	added, problem := w.SubscriptionAdd(w.ctx, w.owner, contract.SubscriptionAddRequest{
		Scope:    contract.ScopeRoom,
		Room:     w.room,
		On:       []contract.EventType{"notice.posted"},
		Workflow: &contract.WorkflowSubscription{Script: goodScript},
	})
	if problem != nil {
		t.Fatalf("SubscriptionAdd with valid meta.host: %v", problem)
	}

	if _, problem := w.SubscriptionUpdate(w.ctx, w.owner, contract.SubscriptionUpdateRequest{
		ID:       added.ID,
		Workflow: &contract.WorkflowSubscription{Script: badScript},
	}); problem == nil || !strings.Contains(problem.Message, "exec-only") {
		t.Fatalf("SubscriptionUpdate with exec-only meta.host: %v", problem)
	}

	// Starting the service pins its run box to meta.host.
	if err := w.serveSubscriptions(w.ctx); err != nil {
		t.Fatalf("serveSubscriptions: %v", err)
	}
	run, err := w.store.ServiceRun(w.ctx, added.ID)
	if err != nil {
		t.Fatalf("ServiceRun: %v", err)
	}
	if pin, err := w.store.SessionPinnedHost(w.ctx, w.room, run.Session); err != nil || pin != "alpha" {
		t.Fatalf("service run pin = %q, want alpha (err=%v)", pin, err)
	}
}

func TestWorkflowReloadKeepsTheRunsHost(t *testing.T) {
	w := setupWorkflowHosts(t)
	metaAlpha := "export const meta = { name: 'sweep', profiles: ['fast'], host: 'alpha' }\nreturn 1\n"
	started, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: metaAlpha})
	if problem != nil {
		t.Fatalf("start: %v", problem)
	}
	_, problem = w.WorkflowReload(w.ctx, w.owner, contract.WorkflowReloadRequest{Run: started.Run, Script: "export const meta = { name: 'sweep', profiles: ['fast'], host: 'beta' }\nreturn 2\n"})
	if problem == nil || !strings.Contains(problem.Message, "meta.host") {
		t.Fatalf("reload changing meta.host: %v", problem)
	}
	if _, problem := w.WorkflowReload(w.ctx, w.owner, contract.WorkflowReloadRequest{Run: started.Run, Script: "export const meta = { name: 'sweep', profiles: ['deep'], host: 'alpha' }\nreturn 2\n"}); problem != nil {
		t.Fatalf("reload keeping meta.host: %v", problem)
	}
}

package notify

import "testing"

// A worker's claim names the same worker in the engine: its run and agent,
// in the run's room rather than the room the claim carries.
func TestWorkerClaimIsTheSameWorker(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	run := e.newRun("wf_claim", rec, nil)
	claim := ClaimOf(Sender{Kind: SenderWorker, Room: rec.Name, RunID: run.Meta.ID, Agent: 2})
	claim.Room = "elsewhere"
	got, err := e.svc.SenderOf(claim)
	if err != nil || got.Kind != SenderWorker || got.RunID != run.Meta.ID || got.Agent != 2 || got.Room != rec.Name {
		t.Fatalf("SenderOf(%+v) = %+v, %v; want worker 2 of %s in %s", claim, got, err, run.Meta.ID, rec.Name)
	}
	if _, err := e.svc.SenderOf(Claim{Run: run.Meta.ID}); err == nil {
		t.Fatal("a worker claim with no agent was accepted")
	}
}

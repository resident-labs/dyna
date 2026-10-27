package watch

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

// A local-only room has no hosted CI to test a pull request head, so the
// scheduler queues the head itself. Hosted and both modes keep GitHub's run.
func TestLocalOnlyRoomSchedulesEachOpenPRHead(t *testing.T) {
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for mode, want := range map[string]int{localci.ModeLocal: 1, localci.ModeBoth: 0, localci.ModeGitHub: 0} {
		t.Run(mode, func(t *testing.T) {
			dataDir := t.TempDir()
			registry := localci.Store{DataDir: dataDir}
			if err := registry.SetMode(context.Background(), "room-test", mode); err != nil {
				t.Fatal(err)
			}
			open := Record{Kind: "github", Key: "open", Repo: "owner/repo", HeadRepo: "owner/repo", Head: head, Base: base, Branch: "feature", RoomID: "room-test", State: "open", Watching: true, Polling: Polling{PullHead: head}}
			closed := open
			closed.Key, closed.State = "closed", "closed"
			s := &Service{Store: &Store{Root: state.Root(dataDir)}, LocalCIRegistry: registry}
			jobs, err := s.localOnlyPRHeadJobs(context.Background(), State{Records: map[string]*Record{"open": &open, "closed": &closed}})
			if err != nil || len(jobs) != want {
				t.Fatalf("jobs = %+v, %v; want %d", jobs, err, want)
			}
			if want == 1 && (jobs[0].trial || jobs[0].head != head || jobs[0].base != base) {
				t.Fatalf("job = %+v", jobs[0])
			}
		})
	}
}

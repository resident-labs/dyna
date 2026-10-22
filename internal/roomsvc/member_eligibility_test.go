package roomsvc

import (
	"testing"

	"dyna-agent/internal/subagent"
)

func TestEligibilityMetadataWriteProducesSessionEvent(t *testing.T) {
	for _, name := range []string{"inherited-inboxes", "inherited-receipts", "closed-posts"} {
		t.Run(name, func(t *testing.T) {
			e, rec, sessions, w := seedTargetedMembers(t)
			a, source := sessions[0], sessions[1]
			if _, err := e.mgr.Store.UpdateMeta(a.ID, func(meta *subagent.Meta) {
				switch name {
				case "inherited-inboxes":
					meta.InheritedInboxes = []subagent.InheritedInbox{{Session: source.ID, Address: source.Address(), PendingIDs: []string{"pending"}}}
				case "inherited-receipts":
					meta.InheritedReceipts = map[string]bool{"pending": true}
				case "closed-posts":
					meta.ClosedPosts = map[string]bool{"pending": true}
				}
			}); err != nil {
				t.Fatal(err)
			}
			e.svc.SessionChanged(a.ID)
			changes := 0
			e.svc.scanDirtyMembers(w, func(event Event) {
				if event.Kind == "session" && event.RoomID == rec.ID && event.Change != nil && event.Change.ID == a.ID {
					changes++
				}
			})
			if changes != 1 {
				t.Fatalf("eligibility-only write produced %d session events; want one", changes)
			}
		})
	}
}

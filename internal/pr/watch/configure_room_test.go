package watch

import (
	"strconv"
	"testing"

	"dyna-agent/internal/pr/state"
)

// Both room settings must reach the store in one commit, so one revision step.
func TestConfigureRoomPublishesBothSettingsOnce(t *testing.T) {
	e := newEnv(t)
	const roomID = "room-config"
	if err := e.s.ConfigureRoom(roomID, Reviewer{Profile: "judge", ReviewPublishing: PublishingLocal}); err != nil {
		t.Fatal(err)
	}
	before, err := state.Revision(e.s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.ConfigureRoom(roomID, Reviewer{Profile: "reviewer", ReviewPublishing: PublishingGitHub}); err != nil {
		t.Fatal(err)
	}
	after, err := state.Revision(e.s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := strconv.Atoi(before)
	if after != strconv.Itoa(first+1) {
		t.Fatalf("revision %s -> %s; settings must commit together", before, after)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if cfg := st.Reviewers[roomID]; cfg.Profile != "reviewer" || cfg.ReviewPublishing != PublishingGitHub {
		t.Fatalf("saved settings=%+v", cfg)
	}
}

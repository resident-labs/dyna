package ghclient

import (
	"context"
	"errors"
	"slices"
	"testing"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

// gh pr ready on a PR that the engine holds is refused, and the refusal names the reason and the release command.
func TestPRReadyRefusedWhileEngineHoldsPR(t *testing.T) {
	original := engineHold
	t.Cleanup(func() { engineHold = original })
	engineHold = func(_ context.Context, roomID, prURL string) (*state.Hold, error) {
		if roomID != "room-1" || prURL != "https://github.com/owner/repo/pull/7" {
			t.Fatalf("PRHold(%q, %q), want room-1 and the pull 7 URL", roomID, prURL)
		}
		return &state.Hold{Reason: "wait for the owner"}, nil
	}
	err := refuseHeldReady(context.Background(), room.Record{ID: "room-1"}, "", repository{host: "github.com", name: "owner/repo"}, []string{"pr", "ready", "7"})
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("refuseHeldReady() = %v, want a HeldError", err)
	}
	want := "PR 7 is held in Dyna: wait for the owner. Run dyna git pr release 7 to lift the hold."
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}

// A branch-name target and a bare gh pr ready (the checked-out branch) both reach the hold check.
func TestPRReadyChecksHoldForBranchTargets(t *testing.T) {
	originalHold, originalBranch := engineHold, gitBranch
	t.Cleanup(func() { engineHold, gitBranch = originalHold, originalBranch })
	gitBranch = func(_ context.Context, _ string) (string, error) { return "fix/held", nil }
	var asked []string
	engineHold = func(_ context.Context, _, ref string) (*state.Hold, error) {
		asked = append(asked, ref)
		return &state.Hold{Reason: "wait"}, nil
	}
	rec := room.Record{ID: "room-1"}
	r := repository{host: "github.com", name: "owner/repo"}
	for _, args := range [][]string{{"pr", "ready", "fix/held"}, {"pr", "ready"}} {
		if err := refuseHeldReady(context.Background(), rec, "/checkout", r, args); !errors.As(err, new(*HeldError)) {
			t.Fatalf("refuseHeldReady(%v) = %v, want a HeldError", args, err)
		}
	}
	if want := []string{"fix/held", "fix/held"}; !slices.Equal(asked, want) {
		t.Fatalf("engine asked for %v, want %v", asked, want)
	}
}

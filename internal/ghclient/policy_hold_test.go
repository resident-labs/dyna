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
	engineHold = func(_ context.Context, roomID, repo, ref string) (*state.Hold, error) {
		if roomID != "room-1" || repo != "owner/repo" || ref != "https://github.com/owner/repo/pull/7" {
			t.Fatalf("PRHold(%q, %q, %q), want room-1, owner/repo and the pull 7 URL", roomID, repo, ref)
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

// Every target form reaches the hold check with the repository and PR it names: a
// number, a branch, a bare command, and a -R or --repo flag that is not the target.
func TestPRReadyChecksHoldForEveryTargetForm(t *testing.T) {
	originalHold, originalBranch := engineHold, gitBranch
	t.Cleanup(func() { engineHold, gitBranch = originalHold, originalBranch })
	gitBranch = func(_ context.Context, _ string) (string, error) { return "fix/held", nil }
	var asked []string
	engineHold = func(_ context.Context, _, repo, ref string) (*state.Hold, error) {
		asked = append(asked, repo+" "+ref)
		return &state.Hold{Reason: "wait"}, nil
	}
	rec := room.Record{ID: "room-1"}
	r := repository{host: "github.com", name: "owner/repo"}
	cases := [][]string{
		{"pr", "ready", "fix/held"},
		{"pr", "ready"},
		{"pr", "ready", "--repo", "other/repo", "7"},
	}
	for _, args := range cases {
		var held *HeldError
		if err := refuseHeldReady(context.Background(), rec, "/checkout", r, args); !errors.As(err, &held) {
			t.Fatalf("refuseHeldReady(%v) = %v, want a HeldError", args, err)
		}
	}
	want := []string{
		"owner/repo fix/held",
		"owner/repo fix/held",
		"other/repo https://github.com/other/repo/pull/7",
	}
	if !slices.Equal(asked, want) {
		t.Fatalf("engine asked for %v, want %v", asked, want)
	}
}

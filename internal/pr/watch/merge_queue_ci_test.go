package watch

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

// A repository that publishes no CI for pull request heads cannot supply one, so
// an approved pull request must go to its merge trial instead of waiting at
// "waiting for CI" for ever.
func TestQueueTakesAPRWithoutHeadCIStraightToItsTrial(t *testing.T) {
	mergeable := true
	head := "0123456789abcdef0123456789abcdef01234567"
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Number: 7,
		Head: head, Base: "main", BaseRef: "main", DefaultBranch: "main", Branch: "feature",
		Watching: true, State: "open", CI: "absent", Verdict: "approve", RoundHead: head,
		Mergeable: &mergeable, Created: time.Unix(1000, 0)}
	st := State{
		Records:         map[string]*Record{r.Key: &r},
		RepositoryFacts: map[string]state.RepositoryFacts{defaultRepo(r): {CheckedAt: time.Unix(2000, 0), MergeTrialCI: true}},
	}
	rows := mergeQueue(st, nil)
	if len(rows) != 1 || rows[0].State != "ready" {
		t.Fatalf("queue = %+v, want the pull request ready for its trial", rows)
	}
	// A head whose CI is still running keeps waiting.
	r.CI = "pending"
	st.Records[r.Key] = &r
	if rows := mergeQueue(st, nil); len(rows) != 1 || rows[0].State != "ci" {
		t.Fatalf("pending head = %+v, want it to wait for CI", rows)
	}
}

// The trial gate is per room, because RepositoryFacts holds one answer per
// repository and two rooms can watch the same one. A local-mode room evaluates
// its trial on local CI and needs no hosted push CI; a hosted-mode room still
// does, and neither room's answer may stand in for the other's.
func TestQueueTrialGateIsPerRoom(t *testing.T) {
	mergeable := true
	base := time.Unix(1000, 0)
	record := func(key, room string) *Record {
		head := strings.Repeat(key, 40)[:40]
		return &Record{Kind: "github", Key: key, Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Number: len(key),
			Head: head, Base: "main", BaseRef: "main", DefaultBranch: "main", Branch: "feature", RoomID: room,
			Watching: true, State: "open", CI: "absent", Verdict: "approve", RoundHead: head,
			Mergeable: &mergeable, Created: base}
	}
	local, hosted := record("a", "room_local"), record("b", "room_hosted")
	st := State{
		Records:         map[string]*Record{local.Key: local, hosted.Key: hosted},
		RepositoryFacts: map[string]state.RepositoryFacts{"github.com/owner/repo": {CheckedAt: base, MergeTrialCI: false}},
	}
	rows := mergeQueue(st, func(roomID string) bool { return roomID == "room_local" })
	if len(rows) != 2 {
		t.Fatalf("queue = %+v, want both rooms", rows)
	}
	got := map[string]string{}
	for _, row := range rows {
		got[row.ChangeID] = row.State
	}
	if got[local.Key] != "ready" {
		t.Fatalf("local-mode room = %q, want ready from local CI", got[local.Key])
	}
	if got[hosted.Key] != "trial-unavailable" {
		t.Fatalf("hosted-mode room = %q, want trial-unavailable", got[hosted.Key])
	}
	// The hosted capability alone decides when no room runs local trials.
	if rows := mergeQueue(st, nil); len(rows) != 2 || rows[0].State != "trial-unavailable" || rows[1].State != "trial-unavailable" {
		t.Fatalf("queue without a local room = %+v", rows)
	}
}

// localTrialCIRegistry is a CI node registry whose room runs local-mode CI, so a
// trial of that room is evaluated on local evidence.
type localTrialCIRegistry struct{ hostedCIRegistry }

func (*localTrialCIRegistry) Mode(context.Context, string) (string, error) {
	return localci.ModeLocal, nil
}

// The stored repository fact is the hosted capability, the same answer for every
// room of that repository. A local-mode room must not write its own trial rule
// into it, or a second, hosted-mode room of the same repository would read the
// local answer.
func TestObservedRepositoryFactIgnoresTheRoomMode(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	// A workflow that never triggers on a trial branch, as in a repository
	// without hosted trial CI.
	rewriteMergeDefault(e, func(target map[string]any) {
		target["workflows"] = map[string]any{"object": map[string]any{"entries": []any{
			map[string]any{"name": "ci.yml", "type": "blob", "object": map[string]any{"text": "on: [pull_request]", "isTruncated": false}},
		}}}
	})
	e.s.LocalCIRegistry = &localTrialCIRegistry{}
	if err := e.s.ConfirmHeads(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	facts, ok := st.RepositoryFacts["github.com/owner/repo"]
	if !ok {
		t.Fatalf("no repository facts: %+v", st.RepositoryFacts)
	}
	if facts.MergeTrialCI {
		t.Fatalf("a local-mode room wrote its own trial rule into the repository fact: %+v", facts)
	}
	// The room still reaches its trial, through the per-room gate.
	rows := mergeQueue(st, e.s.LocalTrialRooms(t.Context()))
	if len(rows) != 1 || rows[0].State != "ready" {
		t.Fatalf("queue = %+v, want the local-mode room ready", rows)
	}
}

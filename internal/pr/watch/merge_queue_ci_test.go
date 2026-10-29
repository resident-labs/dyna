package watch

import (
	"context"
	"encoding/json"
	"os"
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
// trial of that room is evaluated on local evidence. It offers one machine and
// no saved result.
type localTrialCIRegistry struct{ hostedCIRegistry }

func (*localTrialCIRegistry) Mode(context.Context, string) (string, error) {
	return localci.ModeLocal, nil
}

func (*localTrialCIRegistry) Nodes(context.Context) ([]localci.Node, error) {
	return []localci.Node{{Name: "hermes", Enabled: true}}, nil
}

func (*localTrialCIRegistry) LoadResult(context.Context, string, string) (localci.Result, error) {
	return localci.Result{}, os.ErrNotExist
}

// headWorkflowsFixture is the workflow list a default-branch head read returns.
type headWorkflowsFixture struct {
	Object struct {
		Entries []headWorkflowFixture `json:"entries"`
	} `json:"object"`
}

// headWorkflowFixture is one workflow file of that list.
type headWorkflowFixture struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Object struct {
		Text        string `json:"text"`
		IsTruncated bool   `json:"isTruncated"`
	} `json:"object"`
}

// withoutTrialPush is a workflow list that never triggers on a trial branch, as
// in a repository without hosted trial CI.
func withoutTrialPush() headWorkflowsFixture {
	entry := headWorkflowFixture{Name: "ci.yml", Type: "blob"}
	entry.Object.Text = "on: [pull_request]"
	fixture := headWorkflowsFixture{}
	fixture.Object.Entries = []headWorkflowFixture{entry}
	return fixture
}

// The stored repository fact is the hosted capability, the same answer for every
// room of that repository. A local-mode room must not write its own trial rule
// into it, or a second, hosted-mode room of the same repository would read the
// local answer.
func TestObservedRepositoryFactIgnoresTheRoomMode(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	workflows, marshalErr := json.Marshal(withoutTrialPush())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	rewriteMergeDefault(e, func(target map[string]any) {
		// rewriteMergeDefault hands back decoded JSON, so the typed fixture has to
		// cross that one untyped boundary as JSON.
		var decoded any
		if err := json.Unmarshal(workflows, &decoded); err != nil {
			t.Fatal(err)
		}
		target["workflows"] = decoded
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
	// The room still reaches its trial, through the per-room gate. A real record
	// names its head repository, which the read above cleared.
	if err := e.s.Store.update(func(st *State) error {
		for _, r := range st.Records {
			r.HeadRepo = r.Repo
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err = e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	rows := mergeQueue(st, e.s.LocalTrialRooms(t.Context()))
	if len(rows) != 1 || rows[0].State != "ready" {
		t.Fatalf("queue = %+v, want the local-mode room ready", rows)
	}
}

// A fork never runs on a CI machine, so a fork has no local trial. Without hosted
// trial CI it must not start a trial that neither provider can finish: it would
// stay pending and hold the repository's trial slot.
func TestQueueTrialGateKeepsForksOutOfTheLocalOverride(t *testing.T) {
	mergeable := true
	record := func(key, headRepo string) *Record {
		head := strings.Repeat(key, 40)[:40]
		return &Record{Kind: "github", Key: key, Host: "github.com", Repo: "owner/repo", HeadRepo: headRepo, Number: len(key),
			Head: head, Base: "main", BaseRef: "main", DefaultBranch: "main", Branch: "feature", RoomID: "room_local",
			Watching: true, State: "open", CI: "absent", Verdict: "approve", RoundHead: head,
			Mergeable: &mergeable, Created: time.Unix(1000, 0)}
	}
	same, fork := record("c", "owner/repo"), record("d", "someone/repo")
	st := State{
		Records:         map[string]*Record{same.Key: same, fork.Key: fork},
		RepositoryFacts: map[string]state.RepositoryFacts{"github.com/owner/repo": {CheckedAt: time.Unix(1000, 0)}},
	}
	rows := mergeQueue(st, func(roomID string) bool { return roomID == "room_local" })
	got := map[string]string{}
	for _, row := range rows {
		got[row.ChangeID] = row.State
	}
	if got[same.Key] != "ready" {
		t.Fatalf("same-repository PR = %q, want ready on local CI", got[same.Key])
	}
	if got[fork.Key] != "trial-unavailable" {
		t.Fatalf("fork PR = %q, want trial-unavailable, because no provider can evaluate it", got[fork.Key])
	}
}

// The real path for a local-mode repository without hosted CI: the head read and
// the gate must reach "absent" instead of waiting for CI that cannot come, so the
// queue takes the approved pull request to its merge trial.
func TestLocalModeRepoWithoutHostedCIReachesItsTrial(t *testing.T) {
	repo, base := changeRepo(t)
	head := changeCommit(t, repo, "head\n")
	e := newEnv(t)
	// The repository publishes no CI for pull request heads.
	e.f.Checks = nil
	e.f.Runs = nil
	// A same-repository pull request at a commit the checkout holds, the only kind
	// the scheduler takes.
	e.f.Pull.Head.Repo.FullName = "owner/repo"
	e.f.Pull.Head.SHA = head
	e.f.Pull.Base.SHA = base
	e.s.LocalCIRegistry = &localTrialCIRegistry{}
	e.track()
	// This fixture serves no workflow list, so the seeded hosted answer stands.
	if err := e.s.Store.update(func(st *State) error {
		st.RepositoryFacts = map[string]RepositoryFacts{"github.com/owner/repo": {
			DefaultBranch: "main", Head: strings.Repeat("d", 40), CheckedAt: e.clock, MergeTrialCI: false,
		}}
		for _, r := range st.Records {
			// A checkout with a local CI definition, so the gate runs its real
			// local-provider path.
			r.Head, r.RoundHead = head, head
			r.HeadRepo, r.RepoPath, r.Base = r.Repo, repo, base
			r.CI, r.Polling.CIVersion = "pending", ""
			r.Verdict, r.RoundHead = "approve", r.Head
			mergeable := true
			r.Mergeable = &mergeable
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A head with no CI is absent once it has been seen long enough to tell a
	// missing workflow from one that has not started.
	for i := 0; i < 6; i++ {
		e.tick()
	}
	r := e.record()
	if r.CI != "absent" {
		t.Fatalf("CI = %q, want absent", r.CI)
	}
	// A real record names its head repository, which the fixture read leaves unset.
	if err := e.s.Store.update(func(st *State) error {
		for _, r := range st.Records {
			r.HeadRepo = r.Repo
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	rows := mergeQueue(st, e.s.LocalTrialRooms(t.Context()))
	if len(rows) != 1 || rows[0].State != "ready" {
		t.Fatalf("queue = %+v, want the approved pull request ready for its trial", rows)
	}
}

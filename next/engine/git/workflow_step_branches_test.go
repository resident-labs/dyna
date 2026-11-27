package git

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs"
	"github.com/resident-labs/dyna/engine/store"
)

func TestWorkflowStepBranchExpiryAgainstBareOrigin(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	const room, repo = "room", "owner/repo"
	end := time.Unix(100, 0)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.CreateRoom(ctx, room, room, end))
	must(st.AddRoomRepo(ctx, room, repo, true))
	event := func(id string, at time.Time, body contract.EventBody) contract.Event {
		return contract.Event{ID: contract.EventID(id), Room: room, Time: at, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body}
	}
	start := func(name string, resume contract.WorkflowRunID) {
		t.Helper()
		session, run := contract.SessionID(name), contract.WorkflowRunID(name)
		_, err := st.StartSession(ctx, store.Session{ID: session, Name: name, Room: room, Repo: repo, Profile: "test", Box: contract.BoxID("box-" + name),
			Workflow: &store.WorkflowRun{ID: run, Name: name, Room: room, Repo: repo, Resume: resume}}, "", contract.BoxSizeAgent, "",
			event("session-"+name, end, contract.SessionStarted{Session: session}), event("box-"+name, end, contract.BoxChanged{Session: session}), "", false, "", "", 0)
		must(err)
		_, err = st.ClaimBranch(ctx, repo, session, name, nil, end)
		must(err)
	}
	finish := func(run contract.WorkflowRunID, at time.Time) {
		t.Helper()
		must(st.EndWorkflow(ctx, run, contract.WorkflowDone, "", event("end-"+string(run), at, contract.WorkflowEnded{Run: run})))
	}

	f := newSeedFixture(t)
	remote := filepath.Join(f.github.root, "owner", "repo.git")
	start("work", "")
	for _, step := range []struct {
		key, branch string
		kind        contract.WorkflowStepKind
	}{
		{"0123456789ab", "work-0123456789ab", contract.WorkflowStepAgent},
		{"1123456789ab", "work-1123456789ab", contract.WorkflowStepAgent}, // moved tip
		{"2123456789ab", "work-2123456789ab", contract.WorkflowStepAgent}, // also a session branch
		{"3123456789ab", "work", contract.WorkflowStepAgent},
		{"4123456789ab", "work-run", contract.WorkflowStepCommand},
		{"5123456789ab", "work-5123456789ab", contract.WorkflowStepAgent}, // tracked open PR head
		{"6123456789ab", "work-6123456789ab", contract.WorkflowStepAgent}, // untracked open PR head
	} {
		_, err := st.StartWorkflowStep(ctx, "work", contract.WorkflowStepStartRequest{Key: step.key, Kind: step.kind}, end)
		must(err)
		must(st.EndWorkflowStep(ctx, "work", contract.WorkflowStepEndRequest{Key: step.key, State: contract.WorkflowStepDone,
			Result: fmt.Sprintf(`{"branch":%q,"commit":%q,"files":[]}`, step.branch, f.first)}, end))
		tip := f.first
		if step.key[0] == '1' {
			tip = f.tip
		}
		runReal(t, remote, "update-ref", "refs/heads/"+step.branch, tip)
	}
	must(st.AddBranch(ctx, repo, "work", "work-2123456789ab", end))
	must(st.RecordPROpen(ctx, "Owner/Repo", 1, "work", "work-5123456789ab", "keep working", "",
		event("pr-open", end, contract.PrOpened{Repo: "Owner/Repo", Number: 1, Session: "work"}), nil))
	finish("work", end)
	list := func() string {
		return runReal(t, remote, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	}
	all := list()
	endpoint, _ := url.Parse("https://api.github.invalid")
	incomplete := true
	checks := 0
	accounts := &prs.Accounts{Endpoint: endpoint, Now: time.Now, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		checks++
		var request struct {
			Query     string
			Variables map[string]string
		}
		defer r.Body.Close()
		must(json.NewDecoder(r.Body).Decode(&request))
		if r.URL.Path != "/graphql" || r.Header.Get("Authorization") != "Bearer test-token" || request.Variables["owner"] != "owner" || request.Variables["name"] != "repo" ||
			!strings.Contains(request.Query, `r0:ref(qualifiedName:"refs/heads/work-0123456789ab"){associatedPullRequests(states:[OPEN]`) ||
			!strings.Contains(request.Query, `r1:ref(qualifiedName:"refs/heads/work-6123456789ab"){associatedPullRequests(states:[OPEN]`) ||
			strings.Contains(request.Query, "work-1123456789ab") || strings.Contains(request.Query, "work-5123456789ab") {
			t.Fatalf("unexpected PR check: %s %s", r.URL.Path, request.Query)
		}
		body := `{"data":{"repository":{"r0":{"associatedPullRequests":{"nodes":[]}},"r1":{"associatedPullRequests":{"nodes":[{"number":16941}]}}}}}`
		if incomplete {
			body = `{"data":{"repository":{"r0":{"associatedPullRequests":{"nodes":[]}}}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	sweep := func(now time.Time, want int) {
		t.Helper()
		branches, err := st.ExpiredWorkflowStepBranches(ctx, now.Add(-WorkflowStepBranchRetention))
		must(err)
		if len(branches) != want {
			t.Fatalf("candidates %+v at %s, want %d", branches, now, want)
		}
		must(DeleteWorkflowStepBranches(ctx, f.github, func(_ context.Context, got contract.RoomID) (string, string, error) {
			if got != room {
				t.Fatalf("credential for room %q", got)
			}
			return "test-token", "", nil
		}, accounts, branches))
	}
	at := end.Add(WorkflowStepBranchRetention)
	sweep(at, 0) // Default off, even past the cutoff.
	must(st.AddRoomRepo(ctx, room, repo, false, store.RoomRepoOptions{StepBranchExpiry: new(true)}))
	must(st.AddRoomRepo(ctx, room, repo, false)) // An omitted option leaves the switch on.
	sweep(at.Add(-time.Millisecond), 0)
	start("child", "work")
	sweep(at, 0) // A copied result in a running descendant keeps its parent's branch.
	finish("child", at)
	at = at.Add(WorkflowStepBranchRetention)
	sweep(at.Add(-time.Millisecond), 0)
	if got := list(); got != all {
		t.Fatalf("before cutoff refs:\n%s", got)
	}
	sweep(at, 3)
	if got := list(); got != all || checks != 1 {
		t.Fatalf("incomplete PR check deleted a head (%d batches):\n%s", checks, got)
	}
	incomplete = false
	sweep(at, 3)
	if got, want := list(), strings.Replace(all, "work-0123456789ab\n", "", 1); got != want || checks != 2 {
		t.Fatalf("after cutoff refs:\n%s\nwant:\n%s", got, want)
	}
	if tip := runReal(t, remote, "rev-parse", "refs/heads/work-1123456789ab"); tip != f.tip {
		t.Fatalf("moved tip %s, want %s", tip, f.tip)
	}
}

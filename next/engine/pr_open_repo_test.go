package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
)

type secondaryPRRefs struct {
	fakeRepos
	available bool
}

func (r secondaryPRRefs) Ref(_ context.Context, repo contract.RepoName, ref string) (string, error) {
	if !r.available || repo != "owner/secondary" || ref != "refs/heads/pr-child" {
		return "", errors.New("ref not found")
	}
	return "abc", nil
}

func TestPrOpenOnAnotherRoomRepository(t *testing.T) {
	r := newPRRig(t)
	const repo contract.RepoName = "owner/secondary"
	ctx, s, child := r.ctx, r.s, r.child
	if err := s.store.AddRoomRepo(ctx, r.room, repo, false); err != nil {
		t.Fatal(err)
	}
	if err := s.store.AddBranch(ctx, "owner/repo", child, "pr-child", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.store.RecordPushedBranches(ctx, "owner/repo", repo, child, []string{"pr-child", "pr-child-topic"}, time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	pull := fakegithub.Pull{Repo: repo, Number: 37, Title: "Secondary", Rollup: "FAILURE", ReviewDecision: "CHANGES_REQUESTED"}
	r.github.Add(pull)
	transport := prPushTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/repos/owner/secondary/pulls" {
			t.Errorf("wrong upstream request: %s %s", req.Method, req.URL.Path)
		}
		var sent contract.PrOpenArgs
		if err := json.NewDecoder(req.Body).Decode(&sent); err != nil || sent.Repo != "" || sent.Head != "pr-child" {
			t.Errorf("wrong request body: %+v, %v", sent, err)
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(`{"number":37}`))}, nil
	})
	// Create through the operation, then let the real poller read the tracked PR and wake the opener.
	entry := s.PrOpen(fakegithub.Token, r.github.Endpoint(), transport)
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"pr.open": entry}, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	open := func(target contract.RepoName, head string) (contract.OpSubmitResponse, *contract.Error) {
		return s.OpSubmit(ctx, sessionCaller(child), contract.OpSubmitRequest{Operation: contract.Operation{Room: r.room, Kind: "pr.open", Args: contract.PrOpenArgs{Repo: target, Title: "Secondary", Base: "main", Head: head}}})
	}
	s.SetRepositories(&secondaryPRRefs{})
	if _, problem := open(repo, ""); problem == nil || problem.Code != contract.ErrInvalid || !strings.Contains(problem.Message, "branch pr-child not found on owner/secondary") {
		t.Fatalf("missing default head: %v", problem)
	}
	s.SetRepositories(&secondaryPRRefs{available: true})
	if _, problem := open("owner/outside", ""); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("outside room: %v", problem)
	}
	if _, problem := open(repo, "someone-elses-branch"); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("unowned head: %v", problem)
	}
	if err := s.store.AddBranch(ctx, repo, r.root, "pr-child-held", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, problem := open(repo, "pr-child-held"); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("head held on the destination: %v", problem)
	}
	result, problem := open(repo, "")
	if problem != nil || result.Operation.State != contract.OpDone || result.Operation.Result.PR == nil || result.Operation.Result.PR.URL != fmt.Sprintf("https://github.com/%s/pull/%d", repo, pull.Number) {
		t.Fatalf("open: %+v, %v", result.Operation.Result, problem)
	}
	tracked, err := s.store.PR(ctx, repo, pull.Number)
	if err != nil || tracked.Session != child || tracked.HeadRepo != repo {
		t.Fatalf("recorded owner: %+v, %v", tracked, err)
	}
	if status, err := s.PrStatus(ctx, sessionCaller(child), contract.PrStatusRequest{Repo: repo, Number: pull.Number}); err != nil || status.PR.Repo != repo || status.PR.Session != child {
		t.Fatalf("status: %+v, %v", status.PR, err)
	}
	r.pass(t)
	if got := r.wakes(t, child); len(got) != 1 || len(got[0].Changes) != 1 || got[0].Changes[0].PR.Repo != repo || got[0].Changes[0].PR.Number != pull.Number {
		t.Fatalf("wakes: %+v", got)
	}
}

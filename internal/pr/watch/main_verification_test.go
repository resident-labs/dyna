package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHeadBatchIncludesDefaultBranchVerification(t *testing.T) {
	for _, state := range []string{"SUCCESS", "FAILURE", "PENDING", ""} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t)
			r := e.track()
			var rollup any
			if state != "" {
				rollup = map[string]any{"state": state, "contexts": map[string]int{"totalCount": 2}}
			}
			branch := map[string]any{"name": "trunk", "target": map[string]any{"oid": strings.Repeat("d", 40), "statusCheckRollup": rollup}}
			node := map[string]any{"number": r.Number, "state": "OPEN", "headRefOid": r.Head}
			body, _ := json.Marshal(map[string]any{"data": map[string]any{"r0": map[string]any{"defaultBranchRef": branch, fmt.Sprint("p", r.Number): node}}})
			calls := 0
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				calls++
				if !strings.Contains(strings.Join(cmd.Args, " "), "oid statusCheckRollup") {
					t.Error("shared batch omitted default-branch checks")
				}
				fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n"+string(body))
				return nil
			}
			probes, _, err := e.s.GH.readHeads(context.Background(), []*Record{&r})
			if err != nil {
				t.Fatal(err)
			}
			got := probes[r.Key].Default
			count := 2
			if state == "" {
				count = 0
			}
			if got.Name != "trunk" || got.Head != strings.Repeat("d", 40) || got.CI != state || got.CheckCount != count || calls != 1 {
				t.Fatalf("default branch fact=%+v; calls=%d", got, calls)
			}
		})
	}
}

func TestDefaultVerificationWaitsForTheNewestCommit(t *testing.T) {
	for _, tc := range []struct {
		name, head, ci string
		count          int
		want           string
	}{
		{"green", strings.Repeat("a", 40), "SUCCESS", 2, "passed"},
		{"red", strings.Repeat("a", 40), "FAILURE", 2, "failed"},
		{"error", strings.Repeat("a", 40), "ERROR", 2, "failed"},
		{"running", strings.Repeat("a", 40), "PENDING", 2, "pending"},
		{"absent checks", strings.Repeat("a", 40), "", 0, "pending"},
		{"empty success is not evidence", strings.Repeat("a", 40), "SUCCESS", 0, "pending"},
		{"missing head", "", "SUCCESS", 2, "pending"},
		{"short head", "abcdef0", "SUCCESS", 2, "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := defaultBranchProbe{Name: "trunk", Head: tc.head, CI: tc.ci, CheckCount: tc.count}
			if got := defaultVerification(p); got != tc.want {
				t.Fatalf("verification=%s; want %s", got, tc.want)
			}
		})
	}
}

func TestMissingDefaultFactsDoNotBlockIndependentHeadPolling(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	body, _ := json.Marshal(map[string]any{"data": map[string]any{"r0": map[string]any{fmt.Sprint("p", r.Number): map[string]any{"number": r.Number, "state": "OPEN", "headRefOid": r.Head}}}})
	e.f.Raw["graphql"] = "HTTP/2.0 200 OK\r\n\r\n" + string(body)
	e.write()
	probes, _, err := e.s.GH.readHeads(t.Context(), []*Record{&r})
	if err != nil || probes[r.Key].Pull.Head.SHA != r.Head {
		t.Fatalf("independent head polling failed: %+v %v", probes, err)
	}
	if got := defaultVerification(probes[r.Key].Default); got != "pending" {
		t.Fatalf("missing default facts allowed merge: %s", got)
	}
}

func TestRepositoryRepresentativesReadPausesOncePerRoom(t *testing.T) {
	st := State{Records: map[string]*Record{}}
	for i := range 500 {
		key := fmt.Sprintf("old-%03d", i)
		st.Records[key] = &Record{Key: key, Kind: "github", Host: "github.com", Repo: "owner/repo-0", Number: i + 1, RoomID: "historical-" + key, State: "merged"}
	}
	for i := range 12 {
		key := fmt.Sprintf("active-%02d", i)
		st.Records[key] = &Record{Key: key, Kind: "github", Host: "github.com", Repo: fmt.Sprintf("owner/repo-%d", i%2), Number: i + 501, RoomID: fmt.Sprintf("room-%d", i%3), State: "open", Watching: true}
	}
	reads := map[string]int{}
	reps, err := repositoryRepresentatives(st, func(room string) (bool, error) { reads[room]++; return room == "room-1", nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 2 || len(reads) != 3 {
		t.Fatalf("representatives=%d room reads=%v", len(reps), reads)
	}
	for room, count := range reads {
		if count != 1 {
			t.Errorf("room %s read %d times", room, count)
		}
	}
}

func TestDefaultBranchIdleReadsHonorSavedRatePause(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	if err := e.s.Store.update(func(st *State) error {
		r := st.Records["github.com/owner/repo#1"]
		r.Watching = false
		r.State = "merged"
		st.LegacyPause = e.clock.Add(time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.s.GH.run = func(context.Context, *exec.Cmd) error {
		t.Fatal("idle default query ignored saved rate pause")
		return nil
	}
	if _, err := e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultBranchRateLimitPersistsForIdleReads(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	if err := e.s.Store.update(func(st *State) error {
		r := st.Records["github.com/owner/repo#1"]
		r.Watching = false
		r.State = "merged"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	limited := 0
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		limited++
		fmt.Fprint(cmd.Stdout, "HTTP/2.0 429 Too Many Requests\r\nRetry-After: 120\r\n\r\n{\"message\":\"slow down\"}")
		return fmt.Errorf("rate limited")
	}
	if _, err := e.s.MergeTick(t.Context()); err == nil {
		t.Fatal("missing rate-limit error")
	}
	st, err := e.s.Store.Read()
	if err != nil || !st.RateLimit.PauseUntil.After(e.clock) {
		t.Fatalf("rate backoff lost: %+v %v", st.RateLimit, err)
	}
	_, _ = e.s.MergeTick(t.Context())
	if limited != 1 {
		t.Fatalf("rate-limited endpoint retried %d times", limited)
	}
}

func TestMergeStatusReadsSavedDefaultFactsWithoutGitHub(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	status := "SUCCESS"
	rewriteMergeDefault(e, func(target map[string]any) {
		target["statusCheckRollup"].(map[string]any)["state"] = status
	})
	for _, tc := range []struct{ remote, want string }{
		{"SUCCESS", "ready"}, {"PENDING", "ready"}, {"FAILURE", "ready"},
	} {
		status = tc.remote
		if err := e.s.ConfirmHeads(t.Context()); err != nil {
			t.Fatal(err)
		}
		saved, err := e.s.Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		f := saved.RepositoryFacts["github.com/owner/repo"]
		wantVerdict := map[string]string{"SUCCESS": "passed", "PENDING": "pending", "FAILURE": "failed"}[tc.remote]
		if f.DefaultBranch != "main" || f.Head == "" || f.Verdict != wantVerdict || f.CheckedAt.IsZero() {
			t.Fatalf("saved fact=%+v", f)
		}
		// A separate reader must keep repository metadata and isolate cached maps.
		saved.RepositoryFacts["github.com/owner/repo"] = RepositoryFacts{}
		saved, err = (&Store{Root: e.s.Store.Root}).Read()
		if err != nil || saved.RepositoryFacts["github.com/owner/repo"] != f {
			t.Fatalf("repository fact round trip=%+v error=%v", saved.RepositoryFacts, err)
		}
		run := e.s.GH.run
		e.s.GH.run = func(context.Context, *exec.Cmd) error {
			t.Error("status started a GitHub request")
			return fmt.Errorf("unexpected GitHub request")
		}
		rows, err := e.s.MergeStatus(t.Context())
		e.s.GH.run = run
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0].State != tc.want {
			t.Fatalf("remote=%s rows=%+v", tc.remote, rows)
		}
		st, err := e.s.Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		if rows := mergeQueue(st); len(rows) != 1 || rows[0].State != tc.want {
			t.Fatalf("saved queue=%+v", rows)
		}
	}
}

func TestMergeQueueWaitsForFirstRepositoryObservation(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	if err := e.s.ConfirmHeads(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	st.RepositoryFacts = nil
	if rows := mergeQueue(st); len(rows) != 1 || rows[0].State != "waiting-repository" {
		t.Fatalf("unconfirmed repository queue=%+v", rows)
	}
}

func TestOlderGreenObservationCannotReplaceFreshFailedMain(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	var calls atomic.Int64
	base := e.clock
	e.s.Now = func() time.Time { return base.Add(time.Duration(calls.Add(1)) * time.Nanosecond) }
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	r := st.Records["github.com/owner/repo#1"]
	oldStarted, releaseOld := make(chan struct{}), make(chan struct{})
	oldDone := make(chan error, 1)
	old := *e.s
	oldGH := e.s.GH.run
	old.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		err := oldGH(ctx, cmd)
		if strings.Contains(strings.Join(cmd.Args, " "), "graphql") {
			close(oldStarted)
			select {
			case <-releaseOld:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return err
	}
	go func() {
		_, err := old.observeHeadBatch(t.Context(), st, []*Record{r}, nil)
		oldDone <- err
	}()
	<-oldStarted
	rewriteMergeDefault(e, func(target map[string]any) {
		target["statusCheckRollup"].(map[string]any)["state"] = "FAILURE"
	})
	// The later refresh completes and saves failed main while the older green
	// response is still held. Release that response only after the fresh save.
	err = e.s.ConfirmHeads(t.Context())
	close(releaseOld)
	oldErr := <-oldDone
	if err != nil || oldErr != nil {
		t.Fatalf("observations: fresh=%v old=%v", err, oldErr)
	}
	saved, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.RepositoryFacts["github.com/owner/repo"].Verdict; got != "failed" {
		t.Fatalf("old green replaced fresh failed main: %s", got)
	}
	if rows := mergeQueue(saved); len(rows) != 1 || rows[0].Main.State != "failed" {
		t.Fatalf("stale observation reopened eligibility: %+v", rows)
	}
}

func TestDefaultReasonDoesNotFormatUnknownBranchOrHead(t *testing.T) {
	for _, probe := range []defaultBranchProbe{{}, {Name: "main"}, {Head: "head"}} {
		for _, status := range []string{"pending", "passed", "failed"} {
			if got := defaultReason(probe, status); got != "waiting for GitHub CI on the default branch" {
				t.Errorf("probe=%+v status=%s reason=%q", probe, status, got)
			}
		}
	}
}

func TestDefaultBranchWorkflowGate(t *testing.T) {
	for _, tc := range []struct {
		name, workflow string
		noPush         bool
	}{
		{"PR only", "on: [pull_request, workflow_dispatch]", true},
		{"push", "on: push", false},
		{"matching branch", "on:\n  push:\n    branches: [dev]", false},
		{"other branch", "on:\n  push:\n    branches: [main]", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			r := e.track()
			branch := map[string]any{"name": "dev", "target": map[string]any{
				"oid": strings.Repeat("d", 40),
				"workflows": map[string]any{"object": map[string]any{"entries": []any{
					map[string]any{"name": "ci.yml", "type": "blob", "object": map[string]any{"text": tc.workflow, "isTruncated": false}},
				}}},
			}}
			node := map[string]any{"number": r.Number, "state": "OPEN", "headRefOid": r.Head}
			body, _ := json.Marshal(map[string]any{"data": map[string]any{"r0": map[string]any{"defaultBranchRef": branch, fmt.Sprint("p", r.Number): node}}})
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n"+string(body))
				return nil
			}
			st, _ := e.s.Store.Read()
			_, err := e.s.observeHeadBatch(t.Context(), st, []*Record{&r}, nil)
			if err != nil {
				t.Fatal(err)
			}
			st, _ = e.s.Store.Read()
			p, status := storedDefault(st, r)
			want := "pending"
			if tc.noPush {
				want = "passed"
			}
			if status != want {
				t.Fatalf("status=%s; want %s", status, want)
			}
			if tc.noPush && defaultReason(p, status) != "no default-branch CI; using PR checks" {
				t.Fatalf("reason=%s", defaultReason(p, status))
			}
		})
	}
}

func TestMergeTrialRequirementDoesNotDependOnDefaultPushCI(t *testing.T) {
	for _, tc := range []struct {
		name, workflow, conclusion string
		merge                      bool
	}{
		{"trial CI without main CI", "on: {push: {branches: ['dyna/merge-trial/**']}, pull_request: {}}", "success", true},
		{"PR only", "on: [pull_request, workflow_dispatch]", "success", false},
		{"main push only", "on: {push: {branches: [main]}}", "success", false},
		{"PR pending", "on: [push, pull_request]", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := mergeTrialEnv(t, 1)
			e := f.e
			f.fs[1].Checks[0].Conclusion = tc.conclusion
			if tc.conclusion == "" {
				f.fs[1].Checks[0].Status = "in_progress"
			}
			rewriteMergeDefault(e, func(target map[string]any) {
				delete(target, "statusCheckRollup")
				target["workflows"] = map[string]any{"object": map[string]any{"entries": []any{map[string]any{"name": "ci.yml", "type": "blob", "object": map[string]any{"text": tc.workflow, "isTruncated": false}}}}}
			})
			f.ci = "completed"
			for range 2 {
				if _, err := e.s.MergeTick(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if (len(*f.merged) == 1) != tc.merge {
				t.Fatalf("merged=%v; want %v", *f.merged, tc.merge)
			}
		})
	}
}

func TestNoPushWorkflowPreservesObservedChecks(t *testing.T) {
	for _, ci := range []string{"FAILURE", "PENDING", "SUCCESS"} {
		t.Run(ci, func(t *testing.T) {
			e, _, _ := mergeEnv(t, 1)
			rewriteMergeDefault(e, func(target map[string]any) {
				target["statusCheckRollup"].(map[string]any)["state"] = ci
				target["workflows"] = nil
			})
			if err := e.s.ConfirmHeads(t.Context()); err != nil {
				t.Fatal(err)
			}
			st, _ := e.s.Store.Read()
			r := st.Records["github.com/owner/repo#1"]
			p, status := storedDefault(st, *r)
			want := map[string]string{"FAILURE": "failed", "PENDING": "pending", "SUCCESS": "passed"}[ci]
			if status != want || strings.Contains(defaultReason(p, status), "using PR checks") {
				t.Fatalf("status=%s reason=%s", status, defaultReason(p, status))
			}
		})
	}
}

// A cancelled push can leave the aggregate red while its replacement still runs.
func TestDefaultVerificationWaitsForUnfinishedChecks(t *testing.T) {
	for _, tc := range []struct {
		name, counts string
		want         string
	}{
		{"replacement running", `"checkRunCountsByState":[{"state":"CANCELLED","count":9},{"state":"IN_PROGRESS","count":1}]`, "pending"},
		{"replacement queued", `"checkRunCountsByState":[{"state":"FAILURE","count":1},{"state":"QUEUED","count":1}]`, "pending"},
		{"replacement waiting", `"checkRunCountsByState":[{"state":"FAILURE","count":1},{"state":"WAITING","count":1}]`, "pending"},
		{"status pending", `"statusContextCountsByState":[{"state":"FAILURE","count":1},{"state":"PENDING","count":1}]`, "pending"},
		{"status expected", `"statusContextCountsByState":[{"state":"FAILURE","count":1},{"state":"EXPECTED","count":1}]`, "pending"},
		{"all final", `"checkRunCountsByState":[{"state":"FAILURE","count":1},{"state":"SUCCESS","count":1}]`, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			r := e.track()
			contexts := map[string]any{"totalCount": 2}
			if err := json.Unmarshal([]byte("{"+tc.counts+"}"), &contexts); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]any{"data": map[string]any{"r0": map[string]any{fmt.Sprint("p", r.Number): map[string]any{"number": r.Number, "state": "OPEN", "headRefOid": r.Head}, "defaultBranchRef": map[string]any{"name": "main", "target": map[string]any{"oid": strings.Repeat("d", 40), "statusCheckRollup": map[string]any{"state": "FAILURE", "contexts": contexts}}}}}})
			if err != nil {
				t.Fatal(err)
			}
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				query := strings.Join(cmd.Args, " ")
				if !strings.Contains(query, "checkRunCountsByState") || !strings.Contains(query, "statusContextCountsByState") {
					t.Error("default branch query omitted completion evidence")
				}
				fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n"+string(body))
				return nil
			}
			probes, _, err := e.s.GH.readHeads(t.Context(), []*Record{&r})
			if err != nil {
				t.Fatal(err)
			}
			if got := defaultVerification(probes[r.Key].Default); got != tc.want {
				t.Fatalf("verification=%s; want %s", got, tc.want)
			}
		})
	}
}

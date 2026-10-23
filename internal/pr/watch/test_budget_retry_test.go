package watch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func artifactRetryFixture(t *testing.T) (*testEnv, Snapshot, ChangePolicy) {
	t.Helper()
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"results.xml": `<testsuite><testcase name="fast" time="1"/></testsuite>`})
	e.track()
	e.write()
	return e, Snapshot{Pull: e.f.Pull, Runs: e.f.Runs}, RoomPolicy(State{}, "")
}

func enforceArtifactRetry(t *testing.T, e *testEnv, ctx context.Context, snap Snapshot, policy ChangePolicy) Record {
	t.Helper()
	if err := e.s.enforceTestBudget(ctx, e.s.GH, e.record(), snap, policy); err != nil {
		t.Fatal(err)
	}
	return e.record()
}

func TestArtifactRetryCancellation(t *testing.T) {
	for _, stage := range []string{"listing", "download"} {
		for _, kind := range []string{"cancel", "expired parent"} {
			t.Run(stage+"/"+kind, func(t *testing.T) {
				e, snap, policy := artifactRetryFixture(t)
				base := e.s.GH.run
				target := "/artifacts?"
				if stage == "download" {
					target = "/artifacts/91/zip"
				}
				for attempt := 0; attempt < 4; attempt++ {
					ctx, cancel := context.WithCancel(context.Background())
					e.s.GH.run = func(commandCtx context.Context, cmd *exec.Cmd) error {
						if strings.Contains(strings.Join(cmd.Args, " "), target) {
							if kind == "expired parent" {
								cancel()
								return errors.New("signal: killed")
							}
							return fmt.Errorf("wrapped cancellation: %w", context.Canceled)
						}
						return base(commandCtx, cmd)
					}
					r := enforceArtifactRetry(t, e, ctx, snap, policy)
					cancel()
					if r.CITestRuns[runToken(snap.Runs[0])] != "" || len(r.CITestErrors) != 0 || r.CITestReportNote != "" || len(r.Notices) != 0 || r.CINoJUnitWarned {
						t.Fatalf("cancellation produced report state: %+v", r)
					}
					if !pendingTestReports(r) || r.CITestRetries[runToken(snap.Runs[0])] != 0 {
						t.Fatalf("cancellation counted a failure or lost pending work: %+v", r.CITestRetries)
					}
				}
				e.s.GH.run = base
				r := enforceArtifactRetry(t, e, context.Background(), snap, policy)
				if r.CITestRuns[runToken(snap.Runs[0])] != "checked" || pendingTestReports(r) || r.CITestReportNote != "" {
					t.Fatalf("report did not recover: %+v", r)
				}
			})
		}
	}
}

func TestArtifactRetryRecoveryAndScope(t *testing.T) {
	e, snap, policy := artifactRetryFixture(t)
	base := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), "/artifacts/91/zip") {
			return errors.New("temporary request error")
		}
		return base(ctx, cmd)
	}
	for _, limit := range []string{"10s", "20s"} {
		policy.CITestMax = limit
		r := enforceArtifactRetry(t, e, context.Background(), snap, policy)
		if r.CITestRetries[runToken(snap.Runs[0])] != 1 || len(r.Notices) != 0 {
			t.Fatalf("retry count crossed policies: %+v", r)
		}
	}
	e.s.GH.run = base
	r := enforceArtifactRetry(t, e, context.Background(), snap, policy)
	if pendingTestReports(r) || len(r.Notices) != 0 || len(r.CITestErrors) != 0 || r.CITestRuns[runToken(snap.Runs[0])] != "checked" {
		t.Fatalf("successful retry retained failure state: %+v", r)
	}
	// A retry for an older workflow attempt must not keep polling active.
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].CITestRetries = map[string]int{"old-run": 2}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if r := enforceArtifactRetry(t, e, context.Background(), snap, policy); pendingTestReports(r) {
		t.Fatalf("obsolete retry remained pending: %+v", r.CITestRetries)
	}
}

func TestArtifactRetryDownloadHTTPHeaders(t *testing.T) {
	for _, protocol := range []string{"HTTP/1.1", "HTTP/2.0"} {
		t.Run(protocol, func(t *testing.T) {
			e, snap, policy := artifactRetryFixture(t)
			raw := e.f.Blobs["repos/owner/repo/actions/artifacts/91/zip"]
			// A real gh 2.102.0 artifact download returned one final HTTP/1.1
			// 200 block after its blob redirect, followed directly by ZIP bytes.
			headers := fmt.Sprintf("%s 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n", protocol, len(raw))
			e.f.Blobs["repos/owner/repo/actions/artifacts/91/zip"] = append([]byte(headers), raw...)
			e.write()
			run := e.s.GH.run
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if strings.Contains(strings.Join(cmd.Args, " "), "/artifacts/91/zip") && !strings.Contains(strings.Join(cmd.Args, " "), "--include") {
					return errors.New("artifact request omitted --include")
				}
				return run(ctx, cmd)
			}
			r := enforceArtifactRetry(t, e, context.Background(), snap, policy)
			if r.CITestRuns[runToken(snap.Runs[0])] != "checked" || r.CITestReportNote != "" || len(r.Notices) != 0 {
				t.Fatalf("final download headers damaged ZIP: %+v", r)
			}
		})
	}
}

func TestArtifactRetryHeadAndRunScope(t *testing.T) {
	for _, scope := range []string{"head", "attempt"} {
		t.Run(scope, func(t *testing.T) {
			e, snap, policy := artifactRetryFixture(t)
			base := e.s.GH.run
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if strings.Contains(strings.Join(cmd.Args, " "), "/artifacts/91/zip") {
					return errors.New("temporary error")
				}
				return base(ctx, cmd)
			}
			enforceArtifactRetry(t, e, context.Background(), snap, policy)
			if scope == "head" {
				snap.Pull.Head.SHA = strings.Repeat("b", 40)
				key := e.record().Key
				if err := e.s.Store.update(func(st *State) error {
					st.Records[key].Head = snap.Pull.Head.SHA
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				snap.Runs[0].Attempt++
				snap.Runs[0].Started = e.clock.Format(time.RFC3339)
			}
			r := enforceArtifactRetry(t, e, context.Background(), snap, policy)
			if len(r.CITestRetries) != 1 || r.CITestRetries[runToken(snap.Runs[0])] != 1 || len(r.Notices) != 0 {
				t.Fatalf("retry count crossed %s: %+v", scope, r)
			}
		})
	}
}

func TestArtifactRetryCancellationKeepsCount(t *testing.T) {
	e, snap, policy := artifactRetryFixture(t)
	base := e.s.GH.run
	failure := errors.New("temporary request error")
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), "/artifacts/91/zip") {
			return failure
		}
		return base(ctx, cmd)
	}
	for _, step := range []struct {
		err   error
		count int
	}{
		{failure, 1},
		{context.Canceled, 1},
		{failure, 2},
	} {
		failure = step.err
		r := enforceArtifactRetry(t, e, context.Background(), snap, policy)
		if r.CITestRetries[runToken(snap.Runs[0])] != step.count || len(r.CITestRuns) != 0 || len(r.Notices) != 0 {
			t.Fatalf("cancellation changed the failure count: %+v", r)
		}
	}
}

func TestArtifactTransientFailureRecoversOnNextPoll(t *testing.T) {
	for _, stage := range []string{"listing", "download"} {
		t.Run(stage, func(t *testing.T) {
			e, snap, policy := artifactRetryFixture(t)
			run := e.s.GH.run
			target := "/artifacts?"
			if stage == "download" {
				target = "/artifacts/91/zip"
			}
			calls := 0
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if strings.Contains(strings.Join(cmd.Args, " "), target) {
					calls++
					if calls == 1 {
						return errors.New("connection reset by peer")
					}
				}
				return run(ctx, cmd)
			}
			first := enforceArtifactRetry(t, e, context.Background(), snap, policy)
			if !pendingTestReports(first) || len(first.CITestRuns) != 0 || len(first.Notices) != 0 {
				t.Fatalf("first failure finished the run: %+v", first)
			}
			recovered := enforceArtifactRetry(t, e, context.Background(), snap, policy)
			if calls != 2 || pendingTestReports(recovered) || recovered.CITestRuns[runToken(snap.Runs[0])] != "checked" || len(recovered.Notices) != 0 {
				t.Fatalf("next poll did not recover: %+v", recovered)
			}
		})
	}
}

func TestArtifactFailurePreservesSafeReasonAfterRetries(t *testing.T) {
	for _, stage := range []string{"listing", "download"} {
		for _, status := range []int{0, 503} {
			t.Run(fmt.Sprintf("%s/%d", stage, status), func(t *testing.T) {
				e, snap, policy := artifactRetryFixture(t)
				var output bytes.Buffer
				previous := log.Writer()
				log.SetOutput(&output)
				defer log.SetOutput(previous)
				run := e.s.GH.run
				target := "/artifacts?"
				if stage == "download" {
					target = "/artifacts/91/zip"
				}
				e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
					if strings.Contains(strings.Join(cmd.Args, " "), target) {
						if status != 0 {
							fmt.Fprintf(cmd.Stdout, "HTTP/1.1 %d Service Unavailable\r\n\r\n{}", status)
						}
						return errors.New("connection reset by peer https://blob.example/zip?sig=private-signature Authorization: Bearer ghp_private_secret")
					}
					return run(ctx, cmd)
				}
				var record Record
				for attempt := 1; attempt <= 3; attempt++ {
					record = enforceArtifactRetry(t, e, context.Background(), snap, policy)
					if attempt < 3 && (len(record.Notices) != 0 || len(record.CITestRuns) != 0) {
						t.Fatalf("attempt %d notified too early", attempt)
					}
				}
				want := "connection reset by peer"
				if status != 0 {
					want = "artifact HTTP status 503"
				}
				for _, text := range []string{record.CITestRuns[runToken(snap.Runs[0])], record.CITestReportNote, record.Notices[0].Text, output.String()} {
					if !strings.Contains(text, want) || strings.Contains(text, "private-signature") || strings.Contains(text, "ghp_private_secret") {
						t.Fatalf("missing safe cause: %s", text)
					}
				}
			})
		}
	}
}

func testArtifactRetryRequestFailures(t *testing.T, stage string) {
	for _, tc := range []struct {
		name    string
		status  int
		headers string
		attempt int
	}{
		{"transport", 0, "", 3},
		{"server", 503, "", 3},
		{"unauthorized", 401, "", 1},
		{"forbidden", 403, "", 1},
		{"missing", 404, "", 1},
		{"gone", 410, "", 1},
		{"rate limit", 429, "", 3},
		{"forbidden rate limit", 403, "X-Ratelimit-Remaining: 0\r\n", 3},
		{"retry after", 403, "Retry-After: 60\r\n", 3},
	} {
		t.Run(stage+"/"+tc.name, func(t *testing.T) {
			e, snap, policy := artifactRetryFixture(t)
			base := e.s.GH.run
			target := "/artifacts?"
			if stage == "download" {
				target = "/artifacts/91/zip"
			}
			calls := 0
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if strings.Contains(strings.Join(cmd.Args, " "), target) {
					calls++
					if tc.status != 0 {
						fmt.Fprintf(cmd.Stdout, "HTTP/2.0 %d Error\r\n%s\r\n{}", tc.status, tc.headers)
					}
					return errors.New("request failed with private stderr")
				}
				return base(ctx, cmd)
			}
			for attempt := 1; attempt <= tc.attempt; attempt++ {
				r := enforceArtifactRetry(t, e, context.Background(), snap, policy)
				if attempt < tc.attempt {
					if len(r.CITestRuns) != 0 || len(r.CITestErrors) != 0 || r.CITestReportNote != "" || len(r.Notices) != 0 || r.CITestRetries[runToken(snap.Runs[0])] != attempt {
						t.Fatalf("attempt %d was not deferred: %+v", attempt, r)
					}
				} else if len(r.CITestRuns) != 1 || len(r.CITestErrors) != 1 || !strings.Contains(r.CITestReportNote, "report unreadable") || len(r.Notices) != 1 || pendingTestReports(r) {
					t.Fatalf("terminal failure was not recorded: %+v", r)
				}
			}
			enforceArtifactRetry(t, e, context.Background(), snap, policy)
			if calls != tc.attempt {
				t.Fatalf("got %d requests, want %d", calls, tc.attempt)
			}
		})
	}
}

func TestArtifactRetryListingFailures(t *testing.T)  { testArtifactRetryRequestFailures(t, "listing") }
func TestArtifactRetryDownloadFailures(t *testing.T) { testArtifactRetryRequestFailures(t, "download") }

func TestArtifactDeadlineRetriesStop(t *testing.T) {
	for _, httpTimeout := range []bool{false, true} {
		t.Run(fmt.Sprint(httpTimeout), func(t *testing.T) {
			e, snap, _ := artifactRetryFixture(t)
			base := e.s.GH.run
			calls := 0
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if strings.Contains(strings.Join(cmd.Args, " "), "/artifacts/91/zip") {
					calls++
					if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) < 89*time.Second {
						t.Fatalf("artifact deadline too short: %v", deadline)
					}
					if httpTimeout {
						fmt.Fprint(cmd.Stdout, "HTTP/2.0 408 Request Timeout\r\n\r\n{}")
						return errors.New("exit status 1")
					}
					return context.DeadlineExceeded
				}
				return base(ctx, cmd)
			}
			for attempt := 0; attempt < 4; attempt++ {
				if err := e.s.runBudgets(context.Background(), pollResult{key: e.record().Key, gh: e.s.GH, snap: snap}, map[string]bool{}); err != nil {
					t.Fatal(err)
				}
				e.clock = e.clock.Add(time.Minute)
			}
			r := e.record()
			if calls != 3 || pendingTestReports(r) || !strings.Contains(r.CITestReportNote, "stopped after 3 failed inspection attempts") {
				t.Fatalf("downloads=%d retries=%v note=%q", calls, r.CITestRetries, r.CITestReportNote)
			}
		})
	}
}

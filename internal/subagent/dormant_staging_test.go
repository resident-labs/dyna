package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/shells"
)

// waitFixtureTurn waits for a real fixture runner without the CLI waiter's
// 150 ms poll interval. Keep both completion conditions: a sealed turn and a
// released turn lock. The remaining tests can still use the public Wait path.
func waitFixtureTurn(t *testing.T, e *testEnv, info Info) Info {
	t.Helper()
	ctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		turn, err := e.m.Store.ReadTurn(info.ID, info.Turns)
		if err != nil {
			t.Fatal(err)
		}
		if turn.Done() && !e.m.Store.lockHeld(info.ID) {
			current, err := e.m.Store.Info(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			return current
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestDormantRunStagingAndPublishedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		close bool
	}{
		{"empty workflow", nil, true},
		{"detached workflow staging", map[string]string{"script.js": "export default 1", "plan.json": "{}", "daemon.log": ""}, true},
		{"interrupted workflow Create", map[string]string{"script.js": "export default 1", "events.jsonl": "", "journal.jsonl": ""}, false},
		{"workflow startup log", map[string]string{"daemon.log": "startup failed"}, true},
		{"empty workflow execution file", map[string]string{"events.jsonl": ""}, false},
		{"workflow journal", map[string]string{"journal.jsonl": "{}\n"}, false},
		{"orphan workflow result", map[string]string{"result.json": "1"}, true},
		{"corrupt published workflow", map[string]string{"meta.json": "{"}, false},
		{"empty published workflow", map[string]string{"meta.json": "null"}, false},
		{"wrong workflow identity", map[string]string{"meta.json": `{"id":"other","status":"ok"}`}, false},
		{"missing workflow status", map[string]string{"meta.json": `{"id":"JOB"}`}, false},
		{"legacy owned live workflow", map[string]string{"meta.json": `{"id":"JOB","session":"OWNER","status":"running","pid":PID}`}, false},
		{"current live workflow", map[string]string{"meta.json": `{"id":"JOB","parentSession":"OWNER","status":"running","pid":PID}`}, false},
		{"foreign live workflow", map[string]string{"meta.json": `{"id":"JOB","parentSession":"ses_20261017-000000-abcdef01","status":"running","pid":PID}`}, true},
		{"finished workflow", map[string]string{"meta.json": `{"id":"JOB","parentSession":"OWNER","status":"ok"}`}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := roomControlEnv(t)
			a, _ := e.start("RESPOND: idle SLEEP:1")
			a = waitFixtureTurn(t, e, a)
			if _, err := e.m.Store.UpdateMeta(a.ID, func(m *Meta) {
				m.LastActivity, m.Finished = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
				m.Status = StatusDormant
			}); err != nil {
				t.Fatal(err)
			}
			job := "wf_20261013-184235-2b655443"
			if tc.name == "orphan workflow result" {
				job += "-journal"
			}
			dir := filepath.Join(filepath.Dir(e.m.Store.root()), "runs", job)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			before := map[string]string{}
			for name, text := range tc.files {
				text = strings.NewReplacer("JOB", job, "OWNER", a.ID, "PID", strconv.Itoa(os.Getpid())).Replace(text)
				before[name] = text
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			e.doneGoals()
			result, err := closeIdle(e.m, e.ctx, a.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			if (result.Succeeded == 1) != tc.close {
				t.Fatalf("clear = %+v, want close=%v", result, tc.close)
			}
			meta, err := e.m.Store.ReadMeta(a.ID)
			if err != nil || (!meta.Closed.IsZero()) != tc.close {
				t.Fatalf("session = %+v, %v", meta, err)
			}
			for name, text := range before {
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(b) != text {
					t.Fatalf("artifact %s changed: %q %v", name, b, err)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != len(before) {
				t.Fatalf("artifact files changed: %v %v", entries, err)
			}
		})
	}
}

func TestDormantRunPublicationLockPreventsPartialScan(t *testing.T) {
	e := roomControlEnv(t)
	a, _ := e.start("SLEEP:1 RESPOND: idle")
	a, _ = e.wait(a.ID, 1)
	e.m.Store.UpdateMeta(a.ID, func(m *Meta) {
		m.LastActivity, m.Finished = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
		m.Status = StatusDormant
	})
	root := filepath.Join(filepath.Dir(e.m.Store.root()), "runs")
	id := "wf_20261013-184235-2b655443"
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Create's publication lock protects both incomplete data and the final meta.
	f, err := os.OpenFile(filepath.Join(root, "publication.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err = os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	done := make(chan RoomSessionMutation, 1)
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("clear worker did not stop")
			}
		}
	})
	e.doneGoals()
	go func() { result, _ := closeIdle(e.m, ctx, a.RoomID); done <- result }()
	select {
	case got := <-done:
		joined = true
		t.Fatalf("scanned an unlocked workflow publication: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	b, _ := json.Marshal(runstore.Meta{ID: id, ParentSession: a.ID, Status: "running", Pid: os.Getpid()})
	if err = os.WriteFile(filepath.Join(dir, "meta.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	select {
	case got := <-done:
		joined = true
		if got.Succeeded != 0 || got.Kept != 1 || got.Failed != 0 || got.Results[0].Reason != RoomSessionKeptResponsibilities || got.Results[0].OK != true {
			t.Fatalf("live job owner closed: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("clear did not finish")
	}
}

func TestMissingShellMetadataKeepsNotExistIdentity(t *testing.T) {
	_, err := (&shells.Store{Root: t.TempDir()}).ReadMeta("sh_20261013-184235-2b655443")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing metadata error = %v", err)
	}
}

func TestDormantUnpublishedEvidenceDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	id := "wf_20261013-184235-2b655443"
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// A dangling published metadata link is damage, not absent publication.
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(dir, "meta.json")); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		err := sessionlife.WithPublication(context.Background(), root, "publication.lock", func() error {
			skipped, err := unpublishedDormantJob(root, id)
			if err != nil {
				return err
			}
			if skipped {
				t.Fatal("ambiguous symlink was excluded")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check()
	if err := os.Remove(filepath.Join(dir, "meta.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(dir, "script.js")); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestDormantShellJobsBlockOnlyTheirOwner(t *testing.T) {
	for _, tc := range []struct {
		name  string
		owner string // OWNER is the dormant session
		ended bool
		close bool
	}{
		{"no shell job", "", false, true},
		{"current live shell", "OWNER", false, false},
		{"finished shell", "OWNER", true, true},
		{"foreign live shell", "ses_20261017-000000-abcdef01", false, true},
		// A damaged legacy record is unknown work, so the session stays open.
		{"damaged legacy shell", "LEGACY", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := roomControlEnv(t)
			// Legacy shell records predate dyna.db, so the damaged one is on
			// disk before anything, such as a session's instructions, opens it.
			if tc.owner == "LEGACY" {
				dir := filepath.Join(filepath.Dir(e.m.Store.root()), "shells", "sh_20261013-184235-2b655443")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			a, _ := e.start("RESPOND: idle SLEEP:1")
			a = waitFixtureTurn(t, e, a)
			if _, err := e.m.Store.UpdateMeta(a.ID, func(m *Meta) {
				m.LastActivity, m.Finished = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
				m.Status = StatusDormant
			}); err != nil {
				t.Fatal(err)
			}
			if tc.owner != "" && tc.owner != "LEGACY" {
				owner := strings.ReplaceAll(tc.owner, "OWNER", a.ID)
				job := shells.Meta{ID: "sh_20261013-184235-2b655443", Owner: owner, Supervisor: os.Getpid(), Cmd: []string{"true"}, Started: time.Now()}
				var exit *shells.Exit
				if tc.ended {
					exit = &shells.Exit{Ended: time.Now()}
				}
				st := shells.Store{Root: filepath.Join(filepath.Dir(e.m.Store.root()), "shells")}
				if err := st.Save(job, exit); err != nil {
					t.Fatal(err)
				}
			}
			e.doneGoals()
			result, err := closeIdle(e.m, e.ctx, a.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			if (result.Succeeded == 1) != tc.close {
				t.Fatalf("clear = %+v, want close=%v", result, tc.close)
			}
		})
	}
}

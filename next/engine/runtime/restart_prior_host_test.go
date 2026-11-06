package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

type priorRestartRunner struct{ workspace, stack, log, fail string }

func (r priorRestartRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	args = append([]string(nil), args...)
	args[len(args)-1] = fmt.Sprintf("export RESTART_WORKSPACE=%q RESTART_STACK=%q RESTART_LOG=%q; export RESTART_FAIL=$(cat %q); ", r.workspace, r.stack, r.log, r.fail) + args[len(args)-1]
	return (&nativePinnedRunner{}).Run(ctx, in, args...)
}
func (f *restartFixture) priorHost(t *testing.T) (workspace, stack, log, fail string) {
	t.Helper()
	root := t.TempDir()
	workspace = filepath.Join(root, "workspace")
	stack = filepath.Join(root, "stack")
	log = filepath.Join(root, "commands")
	fail = filepath.Join(root, "fail")
	for _, dir := range []string{workspace, stack} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "prior-work"), []byte("prior"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(fail, nil, 0600); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,maintained_at) SELECT 'prior','',state,cpus,mem_bytes,disk_bytes,checked_at,maintained_at FROM hosts WHERE name='old';
INSERT INTO room_leftovers(host,room,box,slice) VALUES('prior','r','b','')`)
	h, err := podman.New("user@prior", t.TempDir(), "prior", priorRestartRunner{workspace, stack, log, fail})
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.hosts[contract.HostName("prior")] = h
	return
}

func TestOrdinaryDebtKeepsVolumeRecordsForLaterExplicitRestart(t *testing.T) {
	f := newRestartFixture(t)
	workspace, stack, priorLog, _ := f.priorHost(t)
	f.svc.Reconciler().SetRoomHosts(f.runtime)
	pass := func() {
		t.Helper()
		if err := f.svc.Reconciler().Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.svc.Reconciler().Wait()
	}
	pass()
	oldCommands, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	priorCommands, err := os.ReadFile(priorLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{oldCommands, priorCommands} {
		if strings.Contains(string(raw), "volume rm") || strings.Contains(string(raw), "volume prune") {
			t.Fatalf("ordinary failure debt erased named work: %s", raw)
		}
	}
	for range 4 {
		pass()
	}
	for log, before := range map[string][]byte{f.log: oldCommands, priorLog: priorCommands} {
		after, err := os.ReadFile(log)
		if err != nil || string(after) != string(before) {
			t.Fatalf("retained debt retried host RPC without new owner intent: %s: %s %v", log, after, err)
		}
	}
	debts, err := f.st.BoxLeftovers(context.Background(), "b")
	if err != nil || len(debts) != 2 {
		t.Fatalf("container-only cleanup forgot volume records: %+v %v", debts, err)
	}
	for _, dir := range []string{f.workspace, f.stack} {
		assertRestartFile(t, dir, "old-work", true)
	}
	for _, dir := range []string{workspace, stack} {
		assertRestartFile(t, dir, "prior-work", true)
	}
	if problem := f.restart(context.Background()); problem != nil {
		t.Fatal(problem)
	}
	debts, err = f.st.BoxLeftovers(context.Background(), "b")
	if err != nil || len(debts) != 0 {
		t.Fatalf("explicit all-host cleanup retained debt: %+v %v", debts, err)
	}
	for _, dir := range []string{f.workspace, f.stack} {
		assertRestartFile(t, dir, "old-work", false)
	}
	for _, dir := range []string{workspace, stack} {
		assertRestartFile(t, dir, "prior-work", false)
	}
}

func TestSessionRestartDiscardsEveryKnownHostBeforeReset(t *testing.T) {
	for _, mode := range []string{"success", "prior-stack-failure", "prior-unreachable"} {
		t.Run(mode, func(t *testing.T) {
			f := newRestartFixture(t)
			workspace, stack, _, fail := f.priorHost(t)
			if mode == "prior-stack-failure" {
				if err := os.WriteFile(fail, []byte("stack"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "prior-unreachable" {
				f.exec(t, `UPDATE hosts SET state='unreachable' WHERE name='prior'`)
			}
			before := f.snapshot(t)
			problem := f.restart(context.Background())
			if mode != "success" {
				if problem == nil || problem.Code != contract.ErrUnavailable || !strings.Contains(problem.Message, "prior") {
					t.Fatalf("prior-host error must name its host: %v", problem)
				}
				if after := f.snapshot(t); after != before {
					t.Fatalf("prior-host failure reset row: %s", after)
				}
				oldDebts, err := f.st.Leftovers(context.Background(), "old")
				if err != nil || len(oldDebts) != 0 {
					t.Fatalf("confirmed current debt not retired: %+v %v", oldDebts, err)
				}
				priorDebts, err := f.st.Leftovers(context.Background(), "prior")
				if err != nil || len(priorDebts) != 1 {
					t.Fatalf("unconfirmed prior debt lost: %+v %v", priorDebts, err)
				}
				for _, dir := range []string{f.workspace, f.stack} {
					assertRestartFile(t, dir, "old-work", false)
				}
				assertRestartFile(t, workspace, "prior-work", mode == "prior-unreachable")
				assertRestartFile(t, stack, "prior-work", true)
				if err := os.WriteFile(fail, nil, 0600); err != nil {
					t.Fatal(err)
				}
				f.exec(t, `UPDATE hosts SET state='ready' WHERE name='prior'`)
				problem = f.restart(context.Background())
			}
			if problem != nil {
				t.Fatal(problem)
			}
			for _, dir := range []string{workspace, stack} {
				assertRestartFile(t, dir, "prior-work", false)
			}
			debts, err := f.st.BoxLeftovers(context.Background(), "b")
			if err != nil || len(debts) != 0 {
				t.Fatalf("restart retained host debt: %+v %v", debts, err)
			}
			b, err := f.st.Box(context.Background(), "b")
			if err != nil || b.State != contract.BoxPending {
				t.Fatalf("all-host reset: %+v %v", b, err)
			}
			// A later placement on the prior host starts with empty names, then ordinary
			// recovery preserves the new files rather than inheriting discard authority.
			for _, dir := range []string{workspace, stack} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "new-prior-work"), []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			f.exec(t, `UPDATE boxes SET host='prior',state='creating',want='run',image='',updated_at=21 WHERE id='b'`)
			if err := f.runtime.Remove(context.Background(), "b"); err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{workspace, stack} {
				assertRestartFile(t, dir, "new-prior-work", true)
			}
		})
	}
}

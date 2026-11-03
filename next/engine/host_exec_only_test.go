package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
)

// outHostexec answers a run with its output and its collect run with files.
type outHostexec struct {
	mu    sync.Mutex
	runs  []contract.HostexecRunArgs
	files []byte
	trunc bool
}

func (f *outHostexec) RunHostexec(_ context.Context, a contract.HostexecRunArgs, _ contract.OpID) ([]byte, int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, a)
	if a.Collect {
		return f.files, 0, f.trunc, nil
	}
	return []byte("done\n"), 0, false, nil
}
func (*outHostexec) StopHostexec(context.Context, contract.HostName, string) error { return nil }

func runOn(t *testing.T, host contract.HostName, runner *outHostexec) *contract.OpResult {
	t.Helper()
	s, _, root, ctx := setupRoot(t)
	if err := s.store.AddHost(ctx, box.Host{Name: "omarchy", State: box.HostReady, NoAgents: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	s.SetHostexecRunner(runner)
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	res, problem := s.HostexecRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.HostexecRunRequest{Host: host, Command: "grim $DYNA_OUT/s.png"})
	if problem != nil {
		t.Fatal(problem)
	}
	return res.Operation.Result
}

// A run on an exec-only host imports the desktop environment, and the files it leaves in $DYNA_OUT are stored for the box.
func TestHostexecOnExecOnlyHostImportsDesktopAndCollectsFiles(t *testing.T) {
	runner := &outHostexec{files: []byte("tar bytes")}
	result := runOn(t, "omarchy", runner)
	if len(runner.runs) != 2 || !runner.runs[0].Desktop || runner.runs[0].Collect || !runner.runs[1].Collect || runner.runs[1].Desktop {
		t.Fatalf("runs %+v", runner.runs)
	}
	if result.OutFiles == "" || result.Output != "done\n" {
		t.Fatalf("result %+v", result)
	}
}

// A host that takes boxes gets no desktop environment, and a run that leaves nothing stores no files.
func TestHostexecWithoutFilesOrDesktop(t *testing.T) {
	runner := &outHostexec{}
	result := runOn(t, "host", runner)
	if runner.runs[0].Desktop || result.OutFiles != "" {
		t.Fatalf("runs %+v result %+v", runner.runs, result)
	}
}

// Files over the cap are not copied back, and the output says so.
func TestHostexecOutFilesOverTheCap(t *testing.T) {
	result := runOn(t, "omarchy", &outHostexec{files: []byte("x"), trunc: true})
	if result.OutFiles != "" || result.Output != "done\n\n[files in $DYNA_OUT exceed 8 MiB: not copied back]" {
		t.Fatalf("result %+v", result)
	}
}

// Turning agents or ci on for an exec-only host runs the full check first, and a failed check leaves the host exec only.
func TestHostSetPromotesAnExecOnlyHost(t *testing.T) {
	s, _, _, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if err := s.store.AddHost(ctx, box.Host{Name: "omarchy", State: box.HostReady, NoAgents: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	on := true
	var checked []bool
	var fail error
	s.SetHostPromoter(func(_ context.Context, name contract.HostName, admin bool) error {
		checked = append(checked, admin)
		return fail
	})
	fail = errors.New("host check paths: podman missing")
	if _, problem := s.HostSet(ctx, owner, contract.HostSetRequest{Host: "omarchy", Agents: &on}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("a failed check: %+v", problem)
	}
	if only, _ := s.store.ExecOnly(ctx, "omarchy"); !only {
		t.Fatal("a failed check changed the host")
	}
	fail = nil
	if _, problem := s.HostSet(ctx, owner, contract.HostSetRequest{Host: "omarchy", Agents: &on, OwnerOnlyAdmin: true}); problem != nil {
		t.Fatal(problem)
	}
	if only, _ := s.store.ExecOnly(ctx, "omarchy"); only || len(checked) != 2 || checked[0] || !checked[1] {
		t.Fatalf("exec only %v, checks %v", only, checked)
	}
	// A host with a switch on is checked no more.
	if _, problem := s.HostSet(ctx, owner, contract.HostSetRequest{Host: "omarchy", CI: &on}); problem != nil || len(checked) != 2 {
		t.Fatalf("%v checks %v", problem, checked)
	}
}

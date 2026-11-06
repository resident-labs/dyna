package engine

import (
	"context"
	"errors"
	"strings"
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

func runOn(t *testing.T, host contract.HostName, runner *outHostexec, via ...HostexecRunner) *contract.OpResult {
	t.Helper()
	s, _, root, ctx := setupRoot(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	if err := s.store.AddHost(ctx, box.Host{Name: "omarchy", State: box.HostReady, NoAgents: true, ExecOnly: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	if len(via) > 0 {
		s.SetHostexecRunner(via[0])
	} else {
		s.SetHostexecRunner(runner)
	}
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"hostexec.run": s.HostexecRunEntry()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	res, problem := s.HostexecRun(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.HostexecRunRequest{Host: host, Command: "grim $DYNA_OUT/s.png"})
	if problem != nil {
		t.Fatal(problem)
	}
	return res.Operation.Result
}

// The files it leaves in $DYNA_OUT are stored for the box.
func TestHostexecOnExecOnlyHostCollectsFiles(t *testing.T) {
	runner := &outHostexec{files: []byte("tar bytes")}
	result := runOn(t, "omarchy", runner)
	if len(runner.runs) != 2 || runner.runs[0].Collect || !runner.runs[1].Collect {
		t.Fatalf("runs %+v", runner.runs)
	}
	if result.OutFiles == "" || result.Output != "done\n" {
		t.Fatalf("result %+v", result)
	}
}

// A run that leaves nothing stores no files.
func TestHostexecWithoutFiles(t *testing.T) {
	runner := &outHostexec{}
	result := runOn(t, "host", runner)
	if result.OutFiles != "" {
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
	s, room, _, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if err := s.store.AddHost(ctx, box.Host{Name: "omarchy", State: box.HostReady, NoAgents: true, ExecOnly: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	on := true
	var checked []bool
	var fail error
	s.SetHostPromoter(func(_ context.Context, name contract.HostName, admin bool) error {
		checked = append(checked, admin)
		if fail != nil {
			return fail
		}
		return s.store.SetHostChecked(ctx, box.Host{Name: name, CheckedAt: s.now()})
	})
	fail = errors.New("host check paths: podman missing")
	if _, problem := s.HostSet(ctx, owner, contract.HostSetRequest{Host: "omarchy", Agents: &on}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("a failed check: %+v", problem)
	}
	if only, _ := s.store.ExecOnly(ctx, "omarchy"); !only {
		t.Fatal("a failed check changed the host")
	}
	if events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"host.switches_changed"}); err != nil || len(events) != 0 {
		t.Fatalf("a failed promotion wrote an audit: %+v %v", events, err)
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

// A host with both switches off that was not added exec only stays a full host: no desktop environment, and the full probe.
func TestBothSwitchesOffIsNotExecOnly(t *testing.T) {
	s, _, _, ctx := setup(t)
	if err := s.store.AddHost(ctx, box.Host{Name: "off", State: box.HostReady, NoAgents: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	if only, err := s.store.ExecOnly(ctx, "off"); err != nil || only {
		t.Fatalf("exec only %v %v", only, err)
	}
	list, problem := s.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
	if problem != nil || list.Hosts[len(list.Hosts)-1].ExecOnly {
		t.Fatalf("%+v %v", list, problem)
	}
}

// The folder is collected and removed after a lost run too, and a failed collect is said in the output.
func TestHostexecCollectsAfterALostRunAndReportsFailure(t *testing.T) {
	runner := &lostThenFailHostexec{}
	result := runOn(t, "omarchy", &runner.outHostexec, runner)
	if len(runner.runs) != 2 || !runner.runs[1].Collect {
		t.Fatalf("runs %+v", runner.runs)
	}
	if !strings.Contains(result.Output, "could not be read back") {
		t.Fatalf("output %q", result.Output)
	}
}

type lostThenFailHostexec struct{ outHostexec }

func (f *lostThenFailHostexec) RunHostexec(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID) ([]byte, int, bool, error) {
	f.mu.Lock()
	f.runs = append(f.runs, a)
	f.mu.Unlock()
	if a.Collect {
		return nil, 3, false, nil
	}
	return nil, 0, false, errors.New("ssh: no route")
}

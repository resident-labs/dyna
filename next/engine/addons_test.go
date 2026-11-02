package engine

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/addons"
	"github.com/resident-labs/dyna/engine/approve"
)

func skillSaveArchive(t *testing.T, name string) []byte {
	t.Helper()
	archive := skills.NewArchive()
	if err := archive.Add(skills.File, 0o600, []byte("---\nname: "+name+"\ndescription: A test skill\n---\n")); err != nil {
		t.Fatal(err)
	}
	data, err := archive.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// setupAddons gives the service a data folder, approvals and a source that resolves without a network.
func setupAddons(t *testing.T) (*Service, contract.RoomID, contract.Principal, context.Context, *int) {
	t.Helper()
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir(), HostData: t.TempDir()})
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"addon.global": s.AddonGlobal()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	published := new(int)
	s.SetInstructionsPublisher(func(context.Context) { *published++ })
	s.addonSources = &addons.Sources{Git: func(context.Context, ...string) ([]byte, error) {
		return []byte(strings.Repeat("a", 40) + "\tHEAD\n"), nil
	}}
	return s, room, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, ctx, published
}

func waitForOperation(t *testing.T, s *Service, ctx context.Context, id contract.OpID, state contract.OpState) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		record, err := s.store.Operation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if record.Operation.State == state {
			return
		}
	}
	t.Fatalf("operation %s never reached %s", id, state)
}

func TestASessionStaysInItsRoom(t *testing.T) {
	s, _, worker, ctx, _ := setupAddons(t)
	other, problem := s.RoomCreate(ctx, ownerCaller, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.PackageAdd(ctx, worker, contract.PackageAddRequest{Source: "npm:tools@1.2.3", Room: other.Room}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("%+v", problem)
	}
	if _, problem := s.AddonList(ctx, worker, contract.AddonListRequest{Room: other.Room}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("%+v", problem)
	}
}

// op.submit takes the arguments as a session wrote them, so the operation does not trust them.
func TestAForgedGlobalOperationGetsNowhere(t *testing.T) {
	s, room, worker, ctx, _ := setupAddons(t)
	for _, args := range []contract.AddonGlobalArgs{
		{Kind: contract.AddonPackage, Name: "../../elsewhere", Source: "npm:tools@1.0.0"},
		{Kind: contract.AddonPackage, Name: "github.com/o/r", Source: "https://github.com/o/r"},
		{Kind: contract.AddonSkill, Name: "pdf", Bundle: "not-a-hash"},
		{Kind: "other", Name: "pdf"},
	} {
		if _, err := s.approvals.Submit(ctx, worker, contract.Operation{Room: room, Kind: args.OpKind(), Args: args}); err == nil {
			t.Errorf("%+v was accepted", args)
		}
	}
	// A skill's archive names it, whatever the operation says.
	hash, _, err := s.saveFile(ctx, room, bytes.NewReader(skillSaveArchive(t, "real-name")), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	forged := contract.AddonGlobalArgs{Kind: contract.AddonSkill, Name: "../../elsewhere", Bundle: hash}
	op, err := s.approvals.Submit(ctx, worker, contract.Operation{Room: room, Kind: forged.OpKind(), Args: forged})
	if err != nil {
		t.Fatal(err)
	}
	if _, problem := s.OpDecide(ctx, ownerCaller, contract.OpDecideRequest{ID: op.ID, Approve: true}); problem != nil {
		t.Fatal(problem)
	}
	waitForOperation(t, s, ctx, op.ID, contract.OpDone)
	items, err := s.addons.List("")
	if err != nil || len(items) != 1 || items[0].Name != "real-name" {
		t.Fatalf("%+v %v", items, err)
	}
}

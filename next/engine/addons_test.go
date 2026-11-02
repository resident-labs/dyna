package engine

import (
	"bytes"
	"context"
	"os"
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

func TestASessionAddsToItsRoomAtOnce(t *testing.T) {
	s, room, worker, ctx, published := setupAddons(t)
	added, problem := s.PackageAdd(ctx, worker, contract.PackageAddRequest{Source: "npm:tools@1.2.3"})
	if problem != nil || added.Item == nil || added.Operation != "" || added.Item.Room != room || added.Item.By != "worker" || added.Item.Source != "npm:tools@1.2.3" {
		t.Fatalf("%+v %v", added, problem)
	}
	if _, problem := s.MCPAdd(ctx, worker, contract.MCPAddRequest{Name: "docs", URL: "https://example.test/mcp"}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.SkillSave(ctx, worker, contract.SkillSaveRequest{Archive: skillSaveArchive(t, "pdf")}); problem != nil {
		t.Fatal(problem)
	}
	if *published != 3 {
		t.Fatalf("published %d times", *published)
	}
	list, problem := s.PackageList(ctx, worker, contract.AddonListRequest{})
	if problem != nil || len(list.Items) != 1 || list.Items[0].Name != "tools" {
		t.Fatalf("%+v %v", list, problem)
	}
	list, problem = s.SkillList(ctx, worker, contract.AddonListRequest{})
	if problem != nil || len(list.Items) != 2 || !list.Items[0].BuiltIn || list.Items[1].Name != "pdf" {
		t.Fatalf("%+v %v", list, problem)
	}
	if _, problem := s.MCPRemove(ctx, worker, contract.AddonRemoveRequest{Name: "docs"}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.MCPRemove(ctx, worker, contract.AddonRemoveRequest{Name: "docs"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("%+v", problem)
	}
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

func TestASessionsGlobalAddWaitsForTheOwner(t *testing.T) {
	s, room, worker, ctx, published := setupAddons(t)
	pending, problem := s.SkillSave(ctx, worker, contract.SkillSaveRequest{Archive: skillSaveArchive(t, "shared"), Global: true})
	if problem != nil || pending.Operation == "" || pending.Item != nil {
		t.Fatalf("%+v %v", pending, problem)
	}
	if found, err := s.addons.Has("", contract.AddonSkill, "shared"); err != nil || found {
		t.Fatalf("it did not wait: %v %v", found, err)
	}
	if *published != 0 {
		t.Fatal("published before the owner decided")
	}
	waiting, problem := s.OpList(ctx, ownerCaller, contract.OpListRequest{Room: room})
	if problem != nil || len(waiting.Operations) != 1 {
		t.Fatalf("%+v %v", waiting, problem)
	}
	card := waiting.Operations[0].Card
	if waiting.Operations[0].Class != contract.ClassOwner || !strings.Contains(card.Title, "for every room") || !strings.Contains(card.Body, "A test skill") {
		t.Fatalf("%+v", waiting.Operations[0])
	}
	if _, problem := s.OpDecide(ctx, ownerCaller, contract.OpDecideRequest{ID: pending.Operation, Approve: true}); problem != nil {
		t.Fatal(problem)
	}
	waitForOperation(t, s, ctx, pending.Operation, contract.OpDone)
	items, err := s.addons.List("")
	if err != nil || len(items) != 1 || items[0].By != "worker" || items[0].Name != "shared" {
		t.Fatalf("%+v %v", items, err)
	}
	if *published != 1 {
		t.Fatalf("published %d times", *published)
	}

	// Removing it asks too, and a package and a server ask the same way.
	removal, problem := s.SkillRemove(ctx, worker, contract.AddonRemoveRequest{Name: "shared", Global: true})
	if problem != nil || removal.Operation == "" {
		t.Fatalf("%+v %v", removal, problem)
	}
	if _, problem := s.OpDecide(ctx, ownerCaller, contract.OpDecideRequest{ID: removal.Operation, Approve: true}); problem != nil {
		t.Fatal(problem)
	}
	waitForOperation(t, s, ctx, removal.Operation, contract.OpDone)
	if found, _ := s.addons.Has("", contract.AddonSkill, "shared"); found {
		t.Fatal("still there")
	}
	server, problem := s.MCPAdd(ctx, worker, contract.MCPAddRequest{Name: "docs", Command: "server", Args: []string{"--flag"}, Global: true})
	if problem != nil || server.Operation == "" {
		t.Fatalf("%+v %v", server, problem)
	}
	if _, problem := s.OpDecide(ctx, ownerCaller, contract.OpDecideRequest{ID: server.Operation}); problem != nil {
		t.Fatal(problem)
	}
	if found, _ := s.addons.Has("", contract.AddonMCP, "docs"); found {
		t.Fatal("a denied add landed")
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

func TestTheOwnerAddsGloballyWithoutAsking(t *testing.T) {
	s, room, _, ctx, _ := setupAddons(t)
	added, problem := s.PackageAdd(ctx, ownerCaller, contract.PackageAddRequest{Source: "https://github.com/o/r", Global: true})
	if problem != nil || added.Item == nil || added.Item.By != "owner" || added.Item.Name != "github.com/o/r" || !strings.HasSuffix(added.Item.Source, "@"+strings.Repeat("a", 40)) {
		t.Fatalf("%+v %v", added, problem)
	}
	if _, problem := s.PackageAdd(ctx, ownerCaller, contract.PackageAddRequest{Source: "npm:tools@1.0.0"}); problem == nil {
		t.Fatal("the owner named no scope")
	}
	if _, problem := s.PackageAdd(ctx, ownerCaller, contract.PackageAddRequest{Source: "npm:tools@1.0.0", Room: "missing"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("%+v", problem)
	}
	if _, problem := s.AddonSetEnabled(ctx, ownerCaller, contract.AddonSetEnabledRequest{Kind: contract.AddonPackage, Name: "github.com/o/r"}); problem != nil {
		t.Fatal(problem)
	}
	list, problem := s.AddonList(ctx, ownerCaller, contract.AddonListRequest{})
	if problem != nil || len(list.Items) != 2 || !list.Items[1].Disabled {
		t.Fatalf("%+v %v", list, problem)
	}
	if _, problem := s.AddonSetEnabled(ctx, ownerCaller, contract.AddonSetEnabledRequest{Kind: contract.AddonSkill, Name: "none", Room: room}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("%+v", problem)
	}
}

func TestNamesThatWouldClashAreRefused(t *testing.T) {
	s, _, worker, ctx, _ := setupAddons(t)
	for _, add := range []contract.MCPAddRequest{{Name: "dyna", URL: "https://a.test"}, {Name: "shell", URL: "https://a.test"}, {Name: "bad name", URL: "https://a.test"}, {Name: "both", URL: "https://a.test", Command: "x"}, {Name: "none"}, {Name: "args", URL: "https://a.test", Args: []string{"x"}}, {Name: "ftp", URL: "ftp://a.test"}} {
		if _, problem := s.MCPAdd(ctx, worker, add); problem == nil {
			t.Errorf("%+v was accepted", add)
		}
	}
	if _, problem := s.SkillSave(ctx, worker, contract.SkillSaveRequest{Archive: skillSaveArchive(t, "dyna-extend")}); problem == nil || !strings.Contains(problem.Message, "built-in") {
		t.Fatalf("%+v", problem)
	}
	if _, problem := s.SkillSave(ctx, worker, contract.SkillSaveRequest{}); problem == nil {
		t.Fatal("nothing to save was accepted")
	}
}

func TestADeletedRoomLosesItsAddons(t *testing.T) {
	s, room, worker, ctx, _ := setupAddons(t)
	if _, problem := s.SkillSave(ctx, worker, contract.SkillSaveRequest{Archive: skillSaveArchive(t, "pdf")}); problem != nil {
		t.Fatal(problem)
	}
	if err := s.RemoveRoomData(room); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.layout.Skills(room)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestABoxNoticeWaitsForTheSessionsNextTurn(t *testing.T) {
	s, room, worker, ctx, _ := setupAddons(t)
	if _, problem := s.BoxNotice(ctx, worker, contract.BoxNoticeRequest{Text: "package npm:x@1 did not install"}); problem != nil {
		t.Fatal(problem)
	}
	listed, problem := s.EventsList(ctx, ownerCaller, contract.EventsListRequest{Room: room, Types: []contract.EventType{"session.notice"}, Limit: 10})
	if problem != nil || len(listed.Events) != 1 {
		t.Fatalf("%+v %v", listed, problem)
	}
	if body := listed.Events[0].Body.(contract.SessionNotice); body.Session != worker.Session || !strings.Contains(body.Text, "npm:x@1") {
		t.Fatalf("%+v", body)
	}
	var delivered, wakes int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(wake),0) FROM deliveries WHERE seq=? AND to_id=?", listed.Events[0].Seq, worker.Session).Scan(&delivered, &wakes); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 || wakes != 0 {
		t.Fatalf("%d deliveries, %d wakes", delivered, wakes)
	}
}

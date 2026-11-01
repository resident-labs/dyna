package engine

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/compose"
)

// composeRepo answers for a repository's compose level from a map.
type composeRepo struct {
	files map[string]string
}

func (composeRepo) Cloned(contract.RepoName) bool                    { return true }
func (composeRepo) Refresh(context.Context, contract.RepoName) error { return nil }
func (r composeRepo) ComposeNames(context.Context, contract.RepoName) ([]string, error) {
	var names []string
	for name := range r.files {
		names = append(names, name)
	}
	return names, nil
}
func (r composeRepo) ComposeFile(_ context.Context, _ contract.RepoName, name string) ([]byte, error) {
	data, ok := r.files[name]
	if !ok {
		return nil, nil
	}
	return []byte(data), nil
}
func (composeRepo) ImageFolders(context.Context, contract.RepoName) ([]string, error) {
	return []string{"go", "rust"}, nil
}

const plainCompose = "services:\n  agent: {}\n  db:\n    image: postgres:16\n"

func composeService(t *testing.T, s *Service, global map[string]string, repo map[string]string) {
	t.Helper()
	files := &compose.Files{Layout: box.Layout{Data: t.TempDir()}, Repo: composeRepo{repo}}
	dir := files.Layout.Compose("")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range global {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s.SetCompose(files, func(context.Context, contract.RepoName, string) (contract.BoxSizeName, error) {
		return contract.BoxSizeLarge, nil
	})
}

func TestSessionStartKeepsItsComposeFile(t *testing.T) {
	s, room, _, ctx := setup(t)
	composeService(t, s, map[string]string{"default": plainCompose, "other": plainCompose}, nil)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	for name, tc := range map[string]struct{ request, want string }{
		"leaving it out uses default": {"", "default"},
		"a name chooses a file":       {"other", "other"},
	} {
		t.Run(name, func(t *testing.T) {
			started, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "child-" + tc.want, Profile: "test", Compose: tc.request})
			if e != nil {
				t.Fatal(e)
			}
			session, err := s.store.Session(ctx, started.Session)
			if err != nil || session.Compose != tc.want {
				t.Fatalf("compose %q, %v", session.Compose, err)
			}
		})
	}
}

func TestSessionStartWithNoFileIsPlain(t *testing.T) {
	s, room, _, ctx := setup(t)
	composeService(t, s, nil, nil)
	started, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	session, err := s.store.Session(ctx, started.Session)
	if err != nil || session.Compose != "" {
		t.Fatalf("compose %q, %v", session.Compose, err)
	}
}

func TestSessionStartRefusesABadFileAtStart(t *testing.T) {
	s, room, _, ctx := setup(t)
	composeService(t, s, map[string]string{
		"default": "services:\n  agent: {}\n  db:\n    image: a\n    privileged: true\n",
		"greedy":  "services:\n  agent: {}\n  db:\n    image: a\n    mem_limit: 100g\n",
	}, nil)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	for name, want := range map[string]string{"": "privileged is not allowed", "greedy": "over the session's", "nope": "no file named nope"} {
		_, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Compose: name})
		if e == nil || e.Code != contract.ErrInvalid || !strings.Contains(e.Message, want) {
			t.Errorf("compose %q: got %+v", name, e)
		}
	}
	if _, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Compose: "../x"}); e == nil || e.Code != contract.ErrInvalid {
		t.Errorf("got %+v", e)
	}
}

func TestComposeNameNeedsComposeFiles(t *testing.T) {
	s, room, _, ctx := setup(t)
	if _, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Compose: "x"}); e == nil {
		t.Fatal("started with a file that cannot be read")
	}
}

func TestARoomsRootStartsPlain(t *testing.T) {
	s, _, _, ctx := setup(t)
	composeService(t, s, map[string]string{"default": plainCompose}, nil)
	made, e := s.RoomCreate(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.RoomCreateRequest{Name: "another", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	tree, err := s.store.RoomTree(ctx, made.Room)
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.store.Session(ctx, tree.Root())
	if err != nil || session.Compose != "" {
		t.Fatalf("compose %q, %v", session.Compose, err)
	}
}

func TestAComposeImageSetsTheDefaultSize(t *testing.T) {
	s, room, _, ctx := setup(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	s.SetRepositories(&fakeRepos{size: contract.BoxSizeSmall})
	composeService(t, s, nil, map[string]string{"rust": "services:\n  agent:\n    build: .dyna/images/rust\n"})
	started, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Compose: "rust"})
	if e != nil {
		t.Fatal(e)
	}
	session, err := s.store.Session(ctx, started.Session)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.store.Box(ctx, session.Box)
	if err != nil || b.Size.Name != contract.BoxSizeLarge {
		t.Fatalf("size %q, %v", b.Size.Name, err)
	}
}

func TestComposeListServesASessionAndTheOwner(t *testing.T) {
	s, room, root, ctx := setup(t)
	composeService(t, s, map[string]string{"default": plainCompose, "broken": "services: {}\n"}, nil)
	for name, caller := range map[string]contract.Principal{"owner": {Kind: contract.PrincipalOwner}, "root": {Kind: contract.PrincipalSession, Session: root}} {
		t.Run(name, func(t *testing.T) {
			listed, e := s.ComposeList(ctx, caller, contract.ComposeListRequest{Room: room})
			if e != nil {
				t.Fatal(e)
			}
			if len(listed.Files) != 2 || listed.Files[0].Name != "broken" || listed.Files[0].Problem == "" || listed.Files[1].Name != "default" || listed.Files[1].Level != contract.ComposeGlobal {
				t.Fatalf("%+v", listed)
			}
		})
	}
}

type stackWakeHost struct {
	imageWakeHost
	running string
	wanted  string
	stack   *box.Stack
}

func (h *stackWakeHost) Inspect(_ context.Context, id contract.BoxID) (box.ContainerStatus, bool, error) {
	return box.ContainerStatus{Box: id, Stack: h.running}, true, nil
}

func (h *stackWakeHost) CurrentImage(context.Context, contract.SessionID, contract.HostName) (box.Image, error) {
	return box.Image{Tag: h.image, Stack: &box.Stack{Hash: h.wanted}}, nil
}

func (h *stackWakeHost) Recreate(ctx context.Context, spec BoxSpec, image, oldSlice string) error {
	h.stack = spec.Stack
	h.running = spec.Stack.Hash
	return h.imageWakeHost.Recreate(ctx, spec, image, oldSlice)
}

// A sidecar's image or definition can change while the box's own image does not. It follows the same rule: the box swaps
// at its next start, and the session hears once.
func TestWakeSwapsAStaleStack(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 0)
	h := &stackWakeHost{imageWakeHost: imageWakeHost{image: "image"}, running: "old", wanted: "new"}
	s.boxes = h
	resetLifecycle(s)
	passLifecycle(t, s, ctx)
	wakeSession(t, s, ctx, room, sid)
	passLifecycle(t, s, ctx)
	if h.stack == nil || h.stack.Hash != "new" || h.starts != 0 {
		t.Fatalf("stack %+v, starts %d", h.stack, h.starts)
	}
	var notices int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_id=? AND e.type='session.restarted'", sid).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("notices %d", notices)
	}
}

func TestWakeKeepsACurrentStack(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 0)
	h := &stackWakeHost{imageWakeHost: imageWakeHost{image: "image"}, running: "same", wanted: "same"}
	s.boxes = h
	resetLifecycle(s)
	passLifecycle(t, s, ctx)
	wakeSession(t, s, ctx, room, sid)
	passLifecycle(t, s, ctx)
	if h.stack != nil || h.starts != 1 {
		t.Fatalf("stack %+v, starts %d", h.stack, h.starts)
	}
}

type failingStackBoxes struct {
	dormantHost
}

func (failingStackBoxes) Create(context.Context, BoxSpec) (string, error) {
	return "", box.StackError{Reason: "sidecar db was not healthy within 2m0s"}
}

// A box that cannot start its sidecars says which, not only that creation failed.
func TestAStackFailureReachesTheBoxReason(t *testing.T) {
	s, room, _, ctx := setup(t)
	s.boxes = &failingStackBoxes{}
	resetLifecycle(s)
	started, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	session, err := s.store.Session(ctx, started.Session)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE boxes SET due_at=0 WHERE id=?", session.Box)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		passLifecycle(t, s, ctx)
	}
	b, err := s.store.Box(ctx, session.Box)
	if err != nil || b.State != contract.BoxFailed || !strings.Contains(b.Reason, "not healthy") {
		t.Fatalf("box %+v, %v", b, err)
	}
}

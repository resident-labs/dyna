package roomsvc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
)

func servicesByRole(t *testing.T, e *env, roomName string) map[string]ServiceView {
	t.Helper()
	list, err := e.svc.ListServices(roomName)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ServiceView{}
	for _, view := range list {
		out[view.Role] = view
	}
	return out
}

func TestServicesListAutoProfiles(t *testing.T) {
	e := newEnv(t)
	rec := e.room("svc")
	got := servicesByRole(t, e, rec.Name)
	reviewer := got["reviewer"]
	if reviewer.Name != "PR reviewer" || reviewer.Page != "pull-requests" || reviewer.Scope != "room" || got["librarian"].Role != "librarian" {
		t.Fatalf("services = %+v", got)
	}
	// The reviewer ranks by diagnostics. The librarian row is separate.
	view := got["reviewer"]
	if view.Source != "auto" || view.Pinned != "" || view.Profile != "mock" || view.Reason != "auto: best diagnostics + cost" || view.Sessions != 0 {
		t.Fatalf("auto service = %+v", view)
	}
}

// The profile menu offers what a service can run: services-only profiles
// such as sol, but not subagents-only or disabled ones.
func TestServicesListServiceProfiles(t *testing.T) {
	e := newEnv(t)
	store, err := e.mgr.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	store.Profiles = append(store.Profiles,
		profile.Profile{Name: "sol", Harness: profile.HarnessMock, Uses: []string{profile.UseServices}},
		profile.Profile{Name: "worker", Harness: profile.HarnessMock, Uses: []string{profile.UseAgents}},
		profile.Profile{Name: "off", Harness: profile.HarnessMock, Disabled: true},
	)
	rec := e.room("svc")
	for role, view := range servicesByRole(t, e, rec.Name) {
		if !slices.Equal(view.Profiles, []string{"mock", "sol"}) {
			t.Fatalf("%s profiles = %v, want [mock sol]", role, view.Profiles)
		}
	}
}

func TestServiceProfilePinAndReset(t *testing.T) {
	role := "reviewer"
	e := newEnv(t)
	rec := e.room("svc")
	readPin := func() string {
		t.Helper()
		value, err := e.svc.PRs.PRSettings(rec.Name)
		if err != nil {
			t.Fatal(err)
		}
		return value.ReviewerProfile
	}
	if err := e.svc.SetServiceProfile(ownerCtx, rec.Name, role, "mock"); err != nil {
		t.Fatal(err)
	}
	if pin := readPin(); pin != "mock" {
		t.Fatalf("stored pin = %q", pin)
	}
	view := servicesByRole(t, e, rec.Name)[role]
	if view.Source != "pinned" || view.Pinned != "mock" || view.Reason != "pinned by the room" {
		t.Fatalf("pinned service = %+v", view)
	}
	if err := e.svc.SetServiceProfile(ownerCtx, rec.Name, role, "missing"); err == nil {
		t.Fatal("accepted a missing profile")
	}
	if pin := readPin(); pin != "mock" {
		t.Fatalf("missing profile replaced the pin: %q", pin)
	}
	if err := e.svc.SetServiceProfile(ownerCtx, rec.Name, role, ""); err != nil {
		t.Fatal(err)
	}
	if pin := readPin(); pin != "" {
		t.Fatalf("reset kept the pin: %q", pin)
	}
	view = servicesByRole(t, e, rec.Name)[role]
	if view.Source != "auto" || view.Pinned != "" {
		t.Fatalf("auto again = %+v", view)
	}
}

var ownerCtx = authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner})

func TestServiceProfileRejectsUnknownRole(t *testing.T) {
	e := newEnv(t)
	rec := e.room("svc")
	if err := e.svc.SetServiceProfile(ownerCtx, rec.Name, "nobody", ""); err == nil {
		t.Fatal("unknown service accepted")
	}
}

func TestServicesListKeepsRowsWithBrokenReviewerState(t *testing.T) {
	e := newEnv(t)
	rec := e.room("svc")
	root := e.svc.PRs.Watcher().Store.Root
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	got := servicesByRole(t, e, rec.Name)
	if got["reviewer"].Profile != "" || got["reviewer"].Reason == "" {
		t.Fatalf("broken reviewer = %+v", got)
	}
}

// One caller gets one answer for every service profile. The owner may change
// any room, or every room. A root may change only its own room, never another
// room and never every room. A session may change none.
func TestServiceProfileSameCallerSameAnswer(t *testing.T) {
	e := newEnv(t)
	home := e.room("perm-home")
	other := e.room("perm-other")
	root := authority.WithActor(context.Background(), authority.Actor{Role: authority.Root, Room: home.ID, Session: "ses_20261027-000000-0000root"})
	session := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Room: home.ID, Session: "ses_20261027-000000-0000sess"})
	steps := []struct {
		name  string
		ctx   context.Context
		allow bool
		run   func(context.Context, string) error
	}{
		{"owner other room", ownerCtx, true, func(ctx context.Context, role string) error {
			return e.svc.SetServiceProfile(ctx, other.Name, role, "")
		}},
		{"root own room", root, true, func(ctx context.Context, role string) error {
			return e.svc.SetServiceProfile(ctx, home.Name, role, "")
		}},
		{"root other room", root, false, func(ctx context.Context, role string) error {
			return e.svc.SetServiceProfile(ctx, other.Name, role, "")
		}},
		{"session", session, false, func(ctx context.Context, role string) error {
			return e.svc.SetServiceProfile(ctx, home.Name, role, "")
		}},
		{"owner all rooms", ownerCtx, true, func(ctx context.Context, role string) error {
			return e.svc.SetServiceProfileAll(ctx, role, "")
		}},
		{"root all rooms", root, false, func(ctx context.Context, role string) error {
			return e.svc.SetServiceProfileAll(ctx, role, "")
		}},
	}
	for _, step := range steps {
		libErr := step.run(step.ctx, "library")
		revErr := step.run(step.ctx, "reviewer")
		if (libErr == nil) != (revErr == nil) || (libErr == nil) != step.allow {
			t.Fatalf("%s: library %v, reviewer %v, allow %v", step.name, libErr, revErr, step.allow)
		}
		if libErr != nil && revErr != nil && libErr.Error() != revErr.Error() {
			t.Fatalf("%s: library %q, reviewer %q", step.name, libErr, revErr)
		}
	}
	// All rooms writes every room. A later pin by that room's root does not
	// change the other room.
	for _, role := range []string{"library", "reviewer"} {
		if err := e.svc.SetServiceProfileAll(ownerCtx, role, "mock"); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{home.Name, other.Name} {
		rec, err := e.svc.Notify.Rooms.Resolve(name)
		if err != nil || !rec.LibraryPinSet || rec.LibraryPin != "mock" {
			t.Fatalf("%s library pin = set %v %q (%v)", name, rec.LibraryPinSet, rec.LibraryPin, err)
		}
		cfg, err := e.svc.PRs.PRSettings(name)
		if err != nil || cfg.ReviewerProfile != "mock" {
			t.Fatalf("%s reviewer pin = %q (%v)", name, cfg.ReviewerProfile, err)
		}
	}
	if err := e.svc.SetServiceProfile(root, home.Name, "library", ""); err != nil {
		t.Fatal(err)
	}
	otherRec, err := e.svc.Notify.Rooms.Resolve(other.Name)
	if err != nil || otherRec.LibraryPin != "mock" {
		t.Fatalf("a root pin changed another room: %+v %v", otherRec, err)
	}
}

// D-110: the reviewer profile is a room setting, so the room's own root may
// pin it. Root of another room and plain sessions stay out, and the owner
// keeps pinning any room's service.
func TestServiceProfileAllowsOnlyTheRoomsOwnRoot(t *testing.T) {
	e := newEnv(t)
	rec := e.room("svc-roles")
	foreign := e.room("svc-other")
	root := authority.WithActor(context.Background(), authority.Actor{Role: authority.Root, Session: "ses_20261020-000000-0000abcd", Room: rec.ID})
	if err := e.svc.SetServiceProfile(root, rec.Name, "reviewer", "mock"); err != nil {
		t.Fatalf("the room's own root could not pin: %v", err)
	}
	if err := e.svc.SetServiceProfile(root, rec.Name, "reviewer", ""); err != nil {
		t.Fatalf("the room's own root could not unpin: %v", err)
	}
	for who, actor := range map[string]authority.Actor{
		"a root of another room": {Role: authority.Root, Session: "ses_20261020-000000-0000ffff", Room: foreign.ID},
		"a session":              {Role: authority.Session, Session: "ses_20261020-000000-0000eeee", Room: rec.ID},
	} {
		ctx := authority.WithActor(context.Background(), actor)
		if err := e.svc.SetServiceProfile(ctx, rec.Name, "reviewer", ""); err == nil || !strings.Contains(err.Error(), " only;") {
			t.Errorf("%s pinned the reviewer profile: %v", who, err)
		}
	}
}

func TestListServicesShowsAPinnedLibrarianWaiting(t *testing.T) {
	e := newEnv(t)
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "a.go")
	git("commit", "-qm", "init")
	id, err := repoid.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (librarianstate.Store{DataDir: store.DataDir()}).Update(id.ID, true, func(st *librarianstate.State) error {
		st.Main = id.Main
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := e.svc.Notify.Rooms.Create(room.CreateInput{Name: "lib-wait", Repo: repo, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (librarianstate.Store{DataDir: store.DataDir()}).Update(id.ID, false, func(st *librarianstate.State) error {
		st.RoomID = rec.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.mgr.Profiles = func() (*profile.Store, error) {
		return &profile.Store{Profiles: []profile.Profile{
			{Name: "sonnet", Harness: profile.HarnessMock, Disabled: true},
			{Name: "mock", Harness: profile.HarnessMock, Taste: 1},
		}}, nil
	}
	v, err := settings.Load(settings.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if err = v.Set(settings.KeyLibraryProfile, "sonnet"); err != nil {
		t.Fatal(err)
	}
	if err = v.Set(settings.KeyLibraryEnabled, `["`+id.Main+`"]`); err != nil {
		t.Fatal(err)
	}
	if err = v.Save(); err != nil {
		t.Fatal(err)
	}
	got := servicesByRole(t, e, rec.Name)["librarian"]
	if got.Profile != "sonnet" || got.Pinned != "sonnet" || !strings.HasPrefix(got.Reason, "waiting:") || !strings.Contains(got.Reason, "sonnet") {
		t.Fatalf("enabled library = %+v, want the pin and a waiting reason", got)
	}
	if err = v.Set(settings.KeyLibraryEnabled, `[]`); err != nil {
		t.Fatal(err)
	}
	if err = v.Save(); err != nil {
		t.Fatal(err)
	}
	off := servicesByRole(t, e, rec.Name)["librarian"]
	if strings.HasPrefix(off.Reason, "waiting:") {
		t.Fatalf("library off still waits: %+v", off)
	}
}

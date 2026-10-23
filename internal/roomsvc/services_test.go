package roomsvc

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/profile"
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
	if len(got) != 1 || reviewer.Name != "PR reviewer" || reviewer.Page != "pull-requests" || reviewer.Scope != "room" {
		t.Fatalf("services = %+v", got)
	}
	for _, view := range got {
		if view.Source != "auto" || view.Pinned != "" || view.Profile != "mock" || view.Reason != "auto: best taste + cost" || view.Sessions != 0 {
			t.Fatalf("auto service = %+v", view)
		}
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
	if len(got) != 1 || got["reviewer"].Profile != "" || got["reviewer"].Reason == "" {
		t.Fatalf("broken reviewer = %+v", got)
	}
}

// Service profiles are global settings, so only the owner pins them; even
// the room's own root is refused. Before, any session could pin them over
// the engine socket.
func TestServiceProfileIsTheOwnersOnly(t *testing.T) {
	e := newEnv(t)
	rec := e.room("svc-owner")
	for _, role := range []string{"reviewer"} {
		for who, actor := range map[string]authority.Actor{
			"the room's root": {Role: authority.Root, Session: "ses_20261020-000000-0000abcd", Room: rec.ID},
			"a session":       {Role: authority.Session, Session: "ses_20261020-000000-0000ffff", Room: rec.ID},
		} {
			ctx := authority.WithActor(context.Background(), actor)
			if err := e.svc.SetServiceProfile(ctx, rec.Name, role, ""); err == nil || !strings.Contains(err.Error(), " only;") {
				t.Errorf("%s pinned the %s profile: %v", who, role, err)
			}
		}
	}
}

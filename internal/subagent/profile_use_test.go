package subagent

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
)

// Subagent starts refuse a services-only profile, and service starts refuse a
// subagents-only one. The internal root is not a service.
func TestStartFollowsProfileUse(t *testing.T) {
	svc, sub := mockProfile("svc"), mockProfile("sub")
	svc.Uses, sub.Uses = []string{profile.UseServices}, []string{profile.UseAgents}
	e := newTestEnv(t, mockProfile("mock"), svc, sub)
	for _, tc := range []struct {
		name, prof, role string
		service          bool
		refuse           string
	}{
		{"subagent with services-only", "svc", "", false, "limited to Dyna services"},
		{"keeper with subagents-only", "sub", "keeper", false, "limited to subagent sessions"},
		{"service with subagents-only", "sub", "", true, "limited to subagent sessions"},
		{"service with services-only", "svc", "", true, ""},
		{"root with subagents-only", "sub", RoleRoot, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := StartOptions{Goal: "g", Task: "SLEEP:0 RESPOND: ok", Profile: tc.prof, Role: tc.role, Service: tc.service, Dir: t.TempDir()}
			if tc.role == RoleRoot {
				opts.Name = RoleRoot
			}
			_, _, err := e.m.Start(context.Background(), opts)
			if tc.refuse == "" && err != nil {
				t.Fatalf("Start refused: %v", err)
			}
			if tc.refuse != "" && (err == nil || !strings.Contains(err.Error(), tc.refuse)) {
				t.Fatalf("Start = %v, want a refusal with %q", err, tc.refuse)
			}
		})
	}
}

// A service's helper without --profile gets the service default, and the
// internal root's helper is a subagent session even with a subagents-only
// profile.
func TestHelpersPickByTheirParentsUse(t *testing.T) {
	sub, svc := mockProfile("sub"), mockProfile("svc")
	sub.Uses, sub.Default, svc.Uses = []string{profile.UseAgents}, true, []string{profile.UseServices}
	e := newTestEnv(t, sub, svc)
	e.joinRunners()
	start := func(o StartOptions) (Info, error) {
		o.Goal, o.Task, o.Dir = "g", "SLEEP:0 RESPOND: ok", t.TempDir()
		info, _, err := e.m.Start(context.Background(), o)
		if err == nil {
			e.wait(info.ID, 1)
		}
		return info, err
	}
	service, err := start(StartOptions{Service: true})
	if err != nil || service.Profile != "svc" {
		t.Fatalf("service start = %+v, %v; want the svc default", service.Meta, err)
	}
	stampParent(t, service)
	helper, err := start(StartOptions{})
	if err != nil || helper.Profile != "svc" || !helper.Service {
		t.Fatalf("service helper = %+v, %v; want the svc default as a service", helper.Meta, err)
	}
	e.owner()
	root, err := start(StartOptions{Role: RoleRoot, Name: RoleRoot, Profile: "sub"})
	if err != nil {
		t.Fatal(err)
	}
	stampParent(t, root)
	child, err := start(StartOptions{Profile: "sub"})
	if err != nil || child.Service {
		t.Fatalf("root helper = %+v, %v; want a subagent session", child.Meta, err)
	}
}

// With only service profiles, a subagent start without --profile fails with
// a clear message, and a service start still works.
func TestOnlyServiceProfiles(t *testing.T) {
	svc := mockProfile("svc")
	svc.Uses = []string{profile.UseServices}
	e := newTestEnv(t, svc)
	if _, _, err := e.m.Start(context.Background(), StartOptions{Goal: "g", Task: "SLEEP:0 RESPOND: ok", Dir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "no usable profile") {
		t.Fatalf("subagent start = %v, want no usable profile", err)
	}
	if _, _, err := e.m.Start(context.Background(), StartOptions{Goal: "g", Task: "SLEEP:0 RESPOND: ok", Dir: t.TempDir(), Service: true}); err != nil {
		t.Fatalf("service start = %v", err)
	}
}

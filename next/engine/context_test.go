package engine

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type snapshotProfiles struct {
	fakeProfiles
	name string
}

func (p *snapshotProfiles) List() []contract.ProfileSummary {
	return []contract.ProfileSummary{{Name: contract.ProfileName(p.name)}}
}

func TestSnapshotReplayAndCap(t *testing.T) {
	s, _, session, ctx := setup(t)
	profiles := &snapshotProfiles{name: "before"}
	s.profiles = profiles
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	hello, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	if strings.Contains(hello.Instructions, "Use dyna post") || !strings.Contains(hello.Instructions, "context_update") {
		t.Fatalf("enabled profile hello has the wrong guidance: %+v", hello)
	}
	for _, key := range []contract.ContextSectionKey{"dyna_goal", "dyna_usage", "invalid"} {
		if _, problem := s.ContextSnapshot(ctx, caller, contract.ContextSnapshotRequest{Section: key}); problem == nil || problem.Code != contract.ErrInvalid {
			t.Fatalf("unavailable section %q: %v", key, problem)
		}
	}
	firstCtx, cancel := context.WithCancel(ctx)
	ch, problem := testChannel(t, s, firstCtx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	start := (<-ch).Body.(contract.TurnStart)
	if len(start.Sections) != 1 || !strings.Contains(start.Sections[0].Text, "before") {
		t.Fatalf("snapshot: %+v", start.Sections)
	}
	profiles.name = "after"
	cancel()
	ch, problem = testChannel(t, s, ctx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	replay := (<-ch).Body.(contract.TurnStart)
	if !reflect.DeepEqual(start.Sections, replay.Sections) {
		t.Fatalf("replay changed: %+v", replay.Sections)
	}
	lines := []string{strings.Repeat("x", 590), strings.Repeat("y", 30)}
	section := renderContextSection("test", lines, 600, false, 0)
	if len([]rune(section.Text)) > 600 || !strings.HasSuffix(section.Text, "2 more: context show") {
		t.Fatalf("cap: %q", section.Text)
	}
	full := renderContextSection("test", lines, 600, true, 0)
	if !strings.Contains(full.Text, lines[1]) {
		t.Fatal("full section was capped")
	}
}

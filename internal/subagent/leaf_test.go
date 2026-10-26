package subagent

import (
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/profile"
)

func TestStartUnderALeafParentIsRefused(t *testing.T) {
	e := newTestEnv(t)
	parent := Meta{ID: NewID(), Name: "leaf-parent", RoomID: e.room.ID, Room: e.room.Name, Depth: 2, Leaf: true}
	if err := e.m.Store.writeMeta(parent); err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(e.ctx, authority.Actor{Role: authority.Session, Session: parent.ID, Room: e.room.ID})
	_, _, err := e.m.Start(ctx, StartOptions{Goal: "g", Task: "RESPOND: done", Profile: "mock", Dir: t.TempDir(), Room: e.room.ID})
	if !errors.Is(err, ErrLeafStart) {
		t.Fatalf("Start under a leaf parent = %v; want %q", err, ErrLeafStart)
	}
}

func TestLeafSessionInstructionsOmitSubagentProfiles(t *testing.T) {
	e := newTestEnv(t, profile.Profile{Name: "helper", Harness: profile.HarnessMock, Uses: []string{profile.UseAgents}})
	meta := Meta{ID: "ses_20261026-000001-aaaaaaaa", Name: "leaf", Room: e.room.Name, RoomID: e.room.ID, Depth: 2, Leaf: true}
	text := e.m.sessionInstructions(meta, "owner", 3, "").Text()
	for _, unwanted := range []string{"Subagent profiles", "[DYNA PROFILES]", "helper"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("leaf instructions mention %q:\n%s", unwanted, text)
		}
	}
	if !strings.Contains(text, "You are a leaf session and cannot start subagents or runs.") {
		t.Fatalf("leaf worker context misses the leaf sentence:\n%s", text)
	}
	meta.Leaf = false
	if plain := e.m.sessionInstructions(meta, "owner", 3, "").Text(); !strings.Contains(plain, "[DYNA PROFILES]") || !strings.Contains(plain, "helper") {
		t.Fatalf("non-leaf instructions lost the profiles section:\n%s", plain)
	}
}

func TestRefuseLeafStart(t *testing.T) {
	e := newTestEnv(t)
	leaf := Meta{ID: NewID(), Name: "leaf", RoomID: e.room.ID, Room: e.room.Name, Depth: 2, Leaf: true}
	if err := e.m.Store.writeMeta(leaf); err != nil {
		t.Fatal(err)
	}
	plain := Meta{ID: NewID(), Name: "plain", RoomID: e.room.ID, Room: e.room.Name, Depth: 2}
	if err := e.m.Store.writeMeta(plain); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		actor authority.Actor
		want  error
	}{
		{name: "leaf session", actor: authority.Actor{Role: authority.Session, Session: leaf.ID}, want: ErrLeafStart},
		{name: "ordinary session", actor: authority.Actor{Role: authority.Session, Session: plain.ID}},
		{name: "unknown session", actor: authority.Actor{Role: authority.Session, Session: NewID()}},
		{name: "owner", actor: authority.Actor{Role: authority.Owner}},
		{name: "worker", actor: authority.Actor{Role: authority.Worker, Run: "wf_1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := RefuseLeafStart(tc.actor)
			if !errors.Is(err, tc.want) {
				t.Fatalf("RefuseLeafStart(%+v) = %v; want %v", tc.actor, err, tc.want)
			}
		})
	}
}

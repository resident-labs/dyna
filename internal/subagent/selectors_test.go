package subagent

import (
	"strings"
	"testing"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
)

func startNamed(e *testEnv, name, task string) Info {
	e.t.Helper()
	info, _ := e.start("SLEEP:1 "+task, func(o *StartOptions) { o.Name = name })
	e.wait(info.ID, 1)
	got, err := e.m.Store.Info(info.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

// The selector line shows what @parent, @siblings, and @children resolve to
// now: every open member, idle or not, and never a member of another group.
func TestSelectorContextResolvesSelectorsNow(t *testing.T) {
	e := newTestEnv(t)
	lead := startNamed(e, "lead", "RESPOND: lead")
	other := startNamed(e, "other", "RESPOND: other")

	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, lead.ID)
	t.Setenv(mode.EnvDepth, "1")
	childA := startNamed(e, "child-a", "RESPOND: a")
	childB := startNamed(e, "child-b", "RESPOND: b")
	t.Setenv(mode.EnvSession, other.ID)
	cousin := startNamed(e, "cousin", "RESPOND: cousin")
	e.owner()

	childMeta, err := e.m.Store.ReadMeta(childA.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := e.m.Store.SelectorContext(childMeta)
	for _, want := range []string{
		"Room: work   You: work:child-a",
		"@parent = work:lead",
		"@siblings = work:child-b (idle)",
		"@children = none",
		"History: dyna room log work (read once if your task depends on earlier decisions; never poll)",
	} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("selector context missing %q:\n%s", want, ctx)
		}
	}
	for _, leak := range []string{cousin.Address(), other.Address(), "Explore:", "Peers"} {
		if strings.Contains(ctx, leak) {
			t.Fatalf("selector context leaked %q:\n%s", leak, ctx)
		}
	}

	leadMeta, err := e.m.Store.ReadMeta(lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	top := e.m.Store.SelectorContext(leadMeta)
	for _, want := range []string{"@parent = @root", "@siblings = work:other (idle)", "work:child-a (idle)", "work:child-b (idle)"} {
		if !strings.Contains(top, want) {
			t.Fatalf("top-level selector context missing %q:\n%s", want, top)
		}
	}
	if strings.Contains(top, childB.Address()+" (idle) +") {
		t.Fatalf("unexpected overflow:\n%s", top)
	}
}

func TestReplyAddressForSenderClasses(t *testing.T) {
	e := newTestEnv(t)
	for from, want := range map[string]string{
		"work:lead":        "work:lead",
		"work":             "work",
		"run:wf_x/agent:2": "run:wf_x/agent:2",
		"shell:42":         "",
		"run:wf_missing":   "",
	} {
		if got := e.m.Store.ReplyAddress(room.Message{From: from}); got != want {
			t.Fatalf("ReplyAddress(%q) = %q, want %q", from, got, want)
		}
	}
	// An engine event has no one to reply to, whatever its sender.
	for _, msg := range []room.Message{
		{From: room.EngineSender, Kind: "session", Notice: room.EventNotice{Kind: "instructions"}},
		{From: room.EngineSender, Kind: "checkin", Notice: room.EventNotice{Kind: "summary"}},
		// TODO: legacy/shim remove later — a check-in stored before the typed
		// events came from the invented system sender. Remove when that
		// history ages out.
		{From: "system:parent-checkin", Kind: "checkin"},
	} {
		if got := e.m.Store.ReplyAddress(msg); got != "" {
			t.Fatalf("ReplyAddress(%+v) = %q, want no reply address", msg, got)
		}
	}
}

// An engine notice reports state, so its delivery header names no reply. The
// engine sender is not a recipient, and its name can also be a room's name, so
// a reply hint would send the agent's answer to the wrong room.
func TestEngineNoticeHeaderNamesNoReply(t *testing.T) {
	e := newTestEnv(t)
	lead := completedFixtureSession(t, e)
	msg, err := e.m.rooms().PostEventInput(room.PostEventInput{
		RoomID:     e.room.ID,
		Event:      room.SessionEvent{Type: room.SessionInstructions, Session: lead.Address(), Detail: "- a changed line"},
		To:         []string{lead.Address()},
		SessionIDs: map[string]string{lead.Address(): lead.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := e.m.Store.ReadMeta(lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	frame := msg.Frame(e.m.Store.frameOptions(meta, msg))
	if !strings.Contains(frame, "[dyna message "+msg.ID+" from "+room.EngineSender+"]") {
		t.Fatalf("frame is not the engine notice:\n%s", frame)
	}
	if strings.Contains(frame, "@"+room.EngineSender) || strings.Contains(frame, "Reply: ") {
		t.Fatalf("engine notice header names a reply:\n%s", frame)
	}
}

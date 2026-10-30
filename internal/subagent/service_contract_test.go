package subagent

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func TestServiceContractUsesOneClassification(t *testing.T) {
	for _, role := range []string{"reviewer", RoleLibrarian, ""} {
		meta := Meta{Service: true, Role: role, ID: "service", RoomID: "room", ParentSession: "parent"}
		contract := meta.ServiceContract()
		if !contract.Active() || !strings.Contains(contract.Prompt(), "Keep posts to the allowed outputs") {
			t.Fatalf("missing contract for %q", role)
		}
		if !contract.CrewTarget(Meta{ID: "parent", RoomID: "room"}) || !contract.CrewTarget(Meta{ID: "child", ParentSession: "service", RoomID: "room"}) {
			t.Fatal("crew missing")
		}
		if contract.CrewTarget(Meta{ID: "parent", RoomID: "other"}) || contract.CrewTarget(Meta{ID: "peer", RoomID: "room"}) {
			t.Fatal("unrelated recipient allowed")
		}
	}
	prompt := (Meta{Service: true, Role: RoleLibrarian}).ServiceContract().Prompt()
	if strings.Contains(prompt, "blocked goal") || !strings.Contains(prompt, "only for a missing profile or a failed dependency that requires owner action") {
		t.Fatalf("unexpected root alert policy: %s", prompt)
	}
	if (Meta{}).ServiceContract().Prompt() != "" {
		t.Fatal("ordinary session gained service rules")
	}
}

func TestServiceTransferRetainsContract(t *testing.T) {
	e, _ := transferEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:0 RESPOND: ready", func(o *StartOptions) { o.Role = "reviewer" })
	info, _ = e.wait(info.ID, 1)
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", WaitSeconds: 0})
	if err != nil {
		t.Fatal(err)
	}
	completeTransfer(t, e, state)
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil || !meta.ServiceContract().Active() || meta.Role != "reviewer" {
		t.Fatalf("contract lost during transfer: %+v %v", meta, err)
	}
}

func TestServiceReplyFrameMatchesDirectMentionRights(t *testing.T) {
	meta := Meta{ID: "service", Name: "reviewer", Room: "work", RoomID: "room", Service: true}
	post := room.Message{ID: "msg_original", From: "work:lead", To: []string{meta.Address()}, Via: []string{"@reviewer"}, SessionIDs: map[string]string{meta.Address(): meta.ID}}
	contract := meta.ServiceContract()
	reply, id := contract.FrameReply(post, post.From)
	frame := post.Header(room.FrameOptions{Room: "work", Reader: meta.Address(), Reply: reply, ReplyTo: id})
	if !strings.Contains(frame, "Reply: dyna post @work:lead --reply-to msg_original") || strings.Contains(frame, "to all") {
		t.Fatal(frame)
	}
	post.Via = []string{"@children"}
	reply, id = contract.FrameReply(post, post.From)
	if reply != "" || id != "" {
		t.Fatal("group created reply rights")
	}
	post.Via = []string{"@reviewer"}
	post.SessionIDs[meta.Address()] = "old-service"
	if contract.DirectMention(post) {
		t.Fatal("reused name inherited reply rights")
	}
}

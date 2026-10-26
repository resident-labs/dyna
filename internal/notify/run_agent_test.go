package notify

import (
	"reflect"
	"testing"

	"dyna-agent/internal/subagent"
)

func TestRunAgentSessionIsNoGroupMemberAndPostsNoReport(t *testing.T) {
	f := newFixture(t)
	info, err := f.mgr.StartRunAgent(f.ctx, subagent.StartOptions{
		Run: &subagent.RunOwner{ID: "wf_x", Agent: 1}, Profile: "mock", Dir: t.TempDir(), Room: f.a.ID, Task: "RESPOND: ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.wait(info.ID, 1)
	offset, err := f.svc.Rooms.FileSize(f.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CompleteSessionTurn(f.ctx, info.ID, 1); err != nil {
		t.Fatal(err)
	}
	if posts := f.postsSince(f.a, offset); len(posts) != 0 {
		t.Fatalf("a run agent's turn end posted %d messages; want none", len(posts))
	}
	got, err := f.svc.resolve(Sender{Kind: SenderOwner, Room: f.a.Name}, []string{"@team"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{f.a.Name + ":crc", f.a.Name + ":lead"}; !reflect.DeepEqual(sorted(got.addrs()), want) {
		t.Fatalf("@team = %v; want %v", got.addrs(), want)
	}
}

func TestPostToARunAgentMentionQueuesOnItsSession(t *testing.T) {
	f := newFixture(t)
	info, err := f.mgr.StartRunAgent(f.ctx, subagent.StartOptions{
		Run: &subagent.RunOwner{ID: "wf_steer", Agent: 3}, Profile: "mock", Dir: t.TempDir(), Room: f.a.ID, Task: "RESPOND: ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.wait(info.ID, 1)
	res, err := f.svc.Post(f.ctx, PostInput{Sender: Sender{Kind: SenderOwner, Room: f.a.Name, Human: true}, Mentions: []string{"@run:wf_steer/agent:3"}, Text: "Use the other branch."})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deliveries) != 1 || res.Deliveries[0].To != info.Address() || res.Deliveries[0].Delivery != "session-queued" {
		t.Fatalf("deliveries = %+v; want the run agent's session queued", res.Deliveries)
	}
	if _, err := f.svc.Post(f.ctx, PostInput{Sender: Sender{Kind: SenderOwner, Room: f.a.Name, Human: true}, Mentions: []string{"@run:wf_steer/agent:4"}, Text: "x"}); err == nil {
		t.Fatal("a post to an agent that has no session was accepted")
	}
}

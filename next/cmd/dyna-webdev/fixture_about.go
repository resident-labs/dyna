package main

import (
	"context"
	"fmt"
	"github.com/resident-labs/dyna/engine/store"
	"log"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
)

var (
	fixtureAsleep  contract.SessionID // reads as a stopped box
	fixtureHot     contract.SessionID // near its limits, so the warning color shows
	fixtureHolding contract.SessionID // holds a list of everything but its goals
)

// seedAbout adds the sessions that exercise the About panel, all invented: one with goals as long as agents write them,
// one with nothing at all, one whose box is stopped, and one running hot. Root's panel takes the hooks of SubscriptionList.
func seedAbout(ctx context.Context, s *engine.Service, room contract.RoomID, hot contract.SessionID) {
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	start := func(name, goal, doneWhen string) contract.SessionID {
		session, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: name, Profile: "fake", Goal: goal, DoneWhen: doneWhen})
		if problem != nil {
			log.Fatal(problem.Message)
		}
		return session.Session
	}
	long := start("launcher-port", "Port the launcher's native profile handling so that ATLAS_LAUNCHER_NATIVE_PROFILE_OVERRIDE and the matching path in src/launcher/profiles/nativeProfileResolver.ts agree on every platform, then remove the old fallback that reads the previous settings file and migrate anyone still on it.",
		"ATLAS_LAUNCHER_NATIVE_PROFILE_OVERRIDE is honoured on Windows, macOS and Linux, the nativeProfileResolver tests cover a missing file, a corrupt file and a file from the previous version, a user on the old settings file keeps their profile after one launch, and nothing in the repository still reads legacy_launcher_settings_v2_path.")
	goals, problem := s.GoalList(ctx, owner, contract.GoalListRequest{Room: room, Session: long})
	if problem != nil {
		log.Fatal(problem.Message)
	}
	goal := goals.Goals[0].ID
	parent := 0
	for i, text := range []string{"Read ATLAS_LAUNCHER_NATIVE_PROFILE_OVERRIDE before the settings file", "Add the corrupt-file test", "Cover a file from the previous version", "Migrate the old settings file on first launch", "Drop the settings importer"} {
		under := 0
		if i == 2 {
			under = parent
		}
		item, problem := s.GoalItem(ctx, owner, contract.GoalItemRequest{Room: room, Goal: goal, Text: text, Parent: under})
		if problem != nil {
			log.Fatal(problem.Message)
		}
		if i == 1 {
			parent = item.Item
		}
		state := map[int]contract.GoalItemState{0: contract.GoalItemDone, 4: contract.GoalItemDropped}[i]
		if state == "" {
			continue
		}
		if _, problem := s.GoalItem(ctx, owner, contract.GoalItemRequest{Room: room, Goal: goal, Item: item.Item, State: state}); problem != nil {
			log.Fatal(problem.Message)
		}
	}
	start("quiet-helper", "", "")
	fixtureAsleep = start("asleep-review", "Review the sync hint copy", "The owner has signed off on the wording.")
	fixtureHot, fixtureHolding = hot, long
}

// SessionResponsibilities adds what a long-running session holds, since nothing in webdev tracks a PR or waits on a question.
func (d devService) SessionResponsibilities(ctx context.Context, p contract.Principal, req contract.SessionResponsibilitiesRequest) (contract.SessionResponsibilitiesResponse, *contract.Error) {
	list, problem := d.Service.SessionResponsibilities(ctx, p, req)
	if problem == nil && req.Session == fixtureHolding {
		owed := func(kind contract.ResponsibilityKind, id string, blocked bool) contract.Responsibility {
			return contract.Responsibility{Kind: kind, Group: contract.ResponsibilityOwesWork, ID: id, Blocked: blocked}
		}
		list.Items = append(list.Items, owed(contract.ResponsibilityPR, "atlas-labs/launcher#6216", false), owed(contract.ResponsibilityPR, "atlas-labs/launcher#6253", false),
			owed(contract.ResponsibilityQuestion, "q4", true), owed(contract.ResponsibilityChild, "prover-1", false), owed(contract.ResponsibilityChild, "prover-2", false), owed(contract.ResponsibilityChild, "docs-pass", false))
	}
	return list, problem
}

// BoxStats invents a reading, because a fake box has no cgroup and its Runner knows no stats message.
func (devService) BoxStats(_ context.Context, _ contract.Principal, req contract.BoxStatsRequest) (contract.BoxStatsResponse, *contract.Error) {
	size, _ := box.Size(contract.BoxSizeMedium)
	response := contract.BoxStatsResponse{Size: size}
	wobble := float64(time.Now().Unix()%7) / 10
	switch req.Session {
	case fixtureAsleep:
	case fixtureHot:
		response.Reading = &contract.BoxReading{MemUsed: 15 * box.GiB / 2, CPUUsed: 3.6 + wobble/10, DiskUsed: 61 * box.GiB}
	default:
		response.Reading = &contract.BoxReading{MemUsed: 5 * box.GiB / 4, CPUUsed: 0.4 + wobble, DiskUsed: 12 * box.GiB}
	}
	return response, nil
}

// SubscriptionList gives the seeded room six hooks and a disabled one, with two runs of one script, as a real room has.
func (d devService) SubscriptionList(ctx context.Context, p contract.Principal, req contract.SubscriptionListRequest) (contract.SubscriptionListResponse, *contract.Error) {
	list, problem := d.Service.SubscriptionList(ctx, p, req)
	if problem != nil || req.Room != fixtureRoom {
		return list, problem
	}
	hook := func(id string, minutes int, command ...string) contract.SubscriptionInfo {
		return contract.SubscriptionInfo{Last: contract.HookPassed, Subscription: contract.Subscription{ID: contract.SubscriptionID(id), Scope: contract.ScopeRoom, Room: req.Room,
			Every: contract.DurationOf(time.Duration(minutes) * time.Minute), Run: &contract.RunSpec{Command: command, Image: "base", Size: contract.BoxSizeSmall}}}
	}
	retired := hook("sub_digest", 1440, "python3", "nightly_digest.py")
	retired.Disabled = true
	list.Subscriptions = append(list.Subscriptions, hook("sub_upstream", 30, "bash", "upstream-watch.sh"), hook("sub_mention", 15, "bash", "mention-watch.sh"),
		hook("sub_author_a", 15, "bash", "author-watch.sh", "mira-chen", "3"), hook("sub_author_b", 15, "bash", "author-watch.sh", "tobias"),
		hook("sub_release", 15, "bash", "release-watch.sh"), hook("sub_sample", 15, "bash", "sample-repo-watch.sh"), retired)
	return list, nil
}

// seedAdvisorCall gives root one turn that asked the advisor, so its transcript shows the advisor row with the prompt and the answer.
func seedAdvisorCall(ctx context.Context, s *engine.Service, st *store.Store, room contract.RoomID, root contract.SessionID) {
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	wake, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: root}, Text: "Which cache should the store use?"})
	if problem != nil {
		log.Fatal(problem.Message)
	}
	input, err := st.EventID(ctx, wake.Seq)
	fatal(err)
	fatal(st.SeedEndedTurn(ctx, room, root, "turn_advisor", input, wake.Seq, time.Now().Add(-5*time.Minute), time.Now().Add(-4*time.Minute)))
	prompt := "Task: pick the cache for the store. I found that reads dominate and the data fits in memory; I lean toward an in-process LRU. What would you check before committing?"
	answer := "Go with the LRU unless the store is read by more than one process.\n\n- **Check first:** if two processes share it, a stale read is the risk.\n- **Unsure:** your read volume."
	lines := fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"user","deliveryId":"%d"}}
{"type":"message","timestamp":%q,"message":{"role":"assistant","content":[{"type":"toolCall","id":"adv1","name":"advisor","arguments":{"prompt":%q}}]}}
{"type":"message","timestamp":%q,"message":{"role":"toolResult","toolCallId":"adv1","content":[{"type":"text","text":%q}]}}
`, time.Now().Add(-5*time.Minute).Format(time.RFC3339), wake.Seq, time.Now().Add(-290*time.Second).Format(time.RFC3339), prompt, time.Now().Add(-270*time.Second).Format(time.RFC3339), answer)
	if _, e := s.TranscriptAppend(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: root}, contract.TranscriptAppendRequest{Path: "advisor.jsonl", Bytes: []byte(lines)}); e != nil {
		log.Fatal(e.Message)
	}
}

// seedAdvisorLive gives root a turn that is asking the advisor now: a [wait] turn, which the fake box keeps open, whose last call has
// no result, and the advisor's agent has read a few files in root's box and is on its next step. The call's row shows its steps and the
// Stop control. It seeds once root's turn has started, which is why it runs apart.
func seedAdvisorLive(ctx context.Context, s *engine.Service, st *store.Store, room contract.RoomID, root contract.SessionID) {
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	wake, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: root}, Text: "[wait] Review the retry change before I merge it."})
	if problem != nil {
		log.Fatal(problem.Message)
	}
	go func() {
		var turn contract.TurnID
		for i := 0; i < 200 && turn == ""; i++ {
			if session, err := st.Session(ctx, root); err == nil {
				turn = session.Turn
			}
			time.Sleep(50 * time.Millisecond)
		}
		if turn == "" {
			log.Print("seedAdvisorLive: root's turn never started")
			return
		}
		started := time.Now().Add(-90 * time.Second)
		at := func(seconds int) string {
			return started.Add(time.Duration(seconds) * time.Second).Format(time.RFC3339)
		}
		prompt := "Task: add retry hints to the sync status. I changed backoff.go and wrote notes.md, which is not committed. Is the backoff right, and does my note match the code?"
		rootLines := fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"user","deliveryId":"%d"}}
{"type":"message","timestamp":%q,"message":{"role":"assistant","content":[{"type":"toolCall","id":"adv_live","name":"advisor","arguments":{"prompt":%q}}]}}
`, at(0), wake.Seq, at(20), prompt)
		rootCaller := contract.Principal{Kind: contract.PrincipalSession, Session: root}
		if _, e := s.TranscriptAppend(ctx, rootCaller, contract.TranscriptAppendRequest{Path: "live.jsonl", Bytes: []byte(rootLines)}); e != nil {
			log.Print(e.Message)
			return
		}
		call := func(second int, id, tool, arguments, result string) string {
			line := fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"assistant","content":[{"type":"toolCall","id":%q,"name":%q,"arguments":%s}]}}`+"\n", at(second), id, tool, arguments)
			if result != "" {
				line += fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"toolResult","toolCallId":%q,"toolName":%q,"content":[{"type":"text","text":%q}]}}`+"\n", at(second+1), id, tool, result)
			}
			return line
		}
		agentLines := fmt.Sprintf(`{"type":"message","timestamp":%q,"message":{"role":"user","deliveryId":"%d"}}`+"\n", at(20), wake.Seq) +
			call(24, "a1", "read", `{"path":"/box/work/sync/notes.md"}`, "Retry hints: wait 2s, then double each time up to 60s.") +
			call(31, "a2", "grep", `{"pattern":"backoff","path":"/box/work/sync"}`, "backoff.go:12: func next(attempt int) time.Duration {\nstatus.go:40: hint := backoff.next(n)") +
			call(38, "a3", "read", `{"path":"/box/work/sync/backoff.go"}`, "func next(attempt int) time.Duration { return min(time.Second<<attempt, time.Minute) }") +
			call(52, "a4", "read", `{"path":"/box/work/sync/status.go"}`, "")
		if _, e := s.TranscriptAppend(ctx, rootCaller, contract.TranscriptAppendRequest{Path: "agents/advisor/live.jsonl", Bytes: []byte(agentLines)}); e != nil {
			log.Print(e.Message)
			return
		}
		fatal(st.StartAgent(ctx, store.Agent{ID: "adv_live", Session: root, Turn: turn, Name: "advisor", Started: started.Add(20 * time.Second)}))
		fatal(st.AgentFile(ctx, root, "adv_live", "agents/advisor/live.jsonl", 0))
	}()
}

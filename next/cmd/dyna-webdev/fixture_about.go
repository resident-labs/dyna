package main

import (
	"context"
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
	second, problem := s.GoalSet(ctx, owner, contract.GoalSetRequest{Room: room, Session: long, Text: "Keep the release checklist honest: every step in docs/release/CHECKLIST.md runs in CI or says plainly why it cannot, and the nightly build publishes to the staging channel first.",
		DoneWhen: "A dry run of the release goes green from a clean checkout, with no manual step left out of the checklist."})
	if problem != nil {
		log.Fatal(problem.Message)
	}
	goals, problem := s.GoalList(ctx, owner, contract.GoalListRequest{Room: room, Session: long})
	if problem != nil {
		log.Fatal(problem.Message)
	}
	for _, goal := range goals.Goals {
		if goal.ID == second.Goal {
			continue
		}
		for i, text := range []string{"Read ATLAS_LAUNCHER_NATIVE_PROFILE_OVERRIDE before the settings file", "Add the corrupt-file test", "Migrate the old settings file on first launch", "Drop the settings importer"} {
			item, problem := s.GoalItem(ctx, owner, contract.GoalItemRequest{Room: room, Goal: goal.ID, Text: text})
			if problem != nil {
				log.Fatal(problem.Message)
			}
			state := map[int]contract.GoalItemState{0: contract.GoalItemDone, 3: contract.GoalItemDropped}[i]
			if state == "" {
				continue
			}
			if _, problem := s.GoalItem(ctx, owner, contract.GoalItemRequest{Room: room, Goal: goal.ID, Item: item.Item, State: state}); problem != nil {
				log.Fatal(problem.Message)
			}
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
			Every: contract.DurationMS(minutes * 60_000), Run: &contract.RunSpec{Command: command, Image: "base", Size: contract.BoxSizeSmall}}}
	}
	retired := hook("sub_digest", 1440, "python3", "nightly_digest.py")
	retired.Disabled = true
	list.Subscriptions = append(list.Subscriptions, hook("sub_upstream", 30, "bash", "upstream-watch.sh"), hook("sub_mention", 15, "bash", "mention-watch.sh"),
		hook("sub_author_a", 15, "bash", "author-watch.sh", "mira-chen", "3"), hook("sub_author_b", 15, "bash", "author-watch.sh", "tobias"),
		hook("sub_release", 15, "bash", "release-watch.sh"), hook("sub_sample", 15, "bash", "sample-repo-watch.sh"), retired)
	return list, nil
}

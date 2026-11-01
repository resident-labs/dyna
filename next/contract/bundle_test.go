package contract

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

var bundleTime = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func filledGlobalBundle() GlobalBundle {
	return GlobalBundle{
		Version:      BundleV1,
		Rooms:        []RoomName{"dyna"},
		Exported:     bundleTime,
		Source:       BundleSource{Commit: "abc123", Backup: "dyna-final.db", BackupSHA256: "f00d"},
		Instructions: "# Instructions\n\n- report bugs to root\n",
		Settings: SettingsBundle{
			OwnerName: "Ari", RootProfile: "opus", SubagentMaxDepth: 3,
			CheckInInterval: 900000, ShellSilence: 600000,
		},
		Profiles: []ProfileBundle{{
			Name: "terra", Description: "builder", Model: "gpt-5.5", Thinking: ThinkingXHigh, Models: []string{"gpt-5.5"}, Fallback: "kimi-max",
			Taste: 7, Intelligence: 9, Cost: 9, Diagnostics: 10, CompactTokens: 200000, MaxConcurrent: 4, Default: true,
		}},
		Hosts:    []HostBundle{{Name: "hermeslab", SSH: "pip@hermeslab", Labels: []string{"gpu"}, Rooms: []RoomName{"dyna"}}},
		Accounts: []AccountLabel{{Provider: ProviderAnthropic, Label: "max-1", Repos: []RepoName{"resident-labs/dyna"}, Rooms: []RoomName{"dyna"}}},
		Devices: []DeviceBundle{{
			Name: "phone", TokenSHA256: "beef", Created: bundleTime, Expires: bundleTime.Add(time.Hour), Scopes: []DeviceScope{"rooms.read"},
		}},
		Repos: []RepoBundle{{
			Repo: "resident-labs/dyna", DefaultRoom: "dyna", MergePolicy: MergeQueue, CIMode: CIBox,
			Attribution: []AttributionLine{{Kind: AttributionPR, Line: "Made with Dyna"}},
		}},
		Skills: []SkillBundle{{Name: "rea", Description: "reverse engineering", Package: "rea-agents@6.8.0"}},
		Subscriptions: []Subscription{{
			ID: "sub_1", Scope: ScopeMachine, Every: 1800000,
			Run: &RunSpec{Command: []string{"sh", "watch.sh"}, Image: "dyna-base", Size: "small", Files: []RunFile{{Path: "watch.sh", Body: "echo ok"}}},
		}},
		Counts: BundleCounts{Hosts: 1, Accounts: 1, Devices: 1, Subscriptions: 1},
	}
}

func filledRoomBundle() RoomBundle {
	root := ImportedAuthor{Kind: PrincipalSession, Label: "dyna:root"}
	return RoomBundle{
		Version:      BundleV1,
		Instructions: "# Instructions\n\n- upgrade floor\n",
		Name:         "dyna",
		Repos:        []RoomRepo{{Repo: "resident-labs/dyna", Default: true}},
		Limits:       RoomLimits{CPUs: 8, MemBytes: 16 << 30},
		Color:        "teal",
		Paused:       true,
		RootProfile:  "terra",
		Upstreams:    []RoomUpstream{{Repo: "resident-labs/dyna", Upstream: "github.com/resident-labs/dyna"}},
		Goals: []GoalBundle{{
			Label: "g1084", Text: "review", Detail: "brief", Setter: root, Created: bundleTime,
			Amendments: []GoalNote{{At: bundleTime, By: root, Text: "also X"}},
			Items:      []GoalItemBundle{{Label: "i1", Text: "step", State: ItemOpen, Assignee: "dyna:builder"}},
		}},
		Issues: []IssueBundle{{
			Number: 584, Title: "one tree", Body: "details", Files: []IssueFile{{Path: "a.go", Line: 3}}, Filer: root,
			Filed: bundleTime, Goal: "g1084", Assignee: "dyna:builder", GitHubURL: "https://github.com/x/y/issues/1",
			Notes: []IssueNote{{At: bundleTime, By: root, Text: "note"}},
		}},
		NextIssue: 587,
		PRs:       []TrackedPR{{Repo: "resident-labs/dyna", Number: 1283, Author: root, Goal: "g1084", Hold: &PRHandoffHold{Reason: "review", By: root, At: bundleTime}}},
		Questions: []QuestionBundle{{
			Label: "q33", Kind: QuestionTerminal, Asker: root, Text: "mount the drive", Options: []string{"yes"},
			Meanwhile: "waiting", Command: "sudo mount /dev/sdb2", Argv: []string{"sudo", "mount", "/dev/sdb2"}, Cwd: "/tmp", Reason: "mount", Asked: bundleTime,
		}},
		Subscriptions: []Subscription{{
			ID: "sub_2", Scope: ScopeRoom, On: []EventType{"pr.merged"}, Run: &RunSpec{Command: []string{"true"}, Image: "dyna-base", Size: "small"},
		}},
		HookHandoff:  []HookHandoff{{Name: "manual-hook", Fields: []string{"command", "cwd"}}},
		LimitHandoff: []RoomLimitGap{RoomLimitMemoryMaxWithoutHigh},
		RepoHandoff:  []RepoHandoff{{Identity: "example.org/team/repo", Reason: RepoUnsupportedForge}},
		Handoff: []SessionHandoff{{
			Name: "dyna:builder", Profile: "terra", ProfileWarning: ProfileUnsupportedMuse, Parent: "dyna:lead", Goals: []GoalID{"g1084"},
			ArchiveRef: "refs/dyna/archive/builder", LastPost: "done",
		}},
		Counts: BundleCounts{Goals: 1, GoalItems: 1, Issues: 1, PRs: 1, Questions: 1, Sessions: 1},
	}
}

func TestBundleRoundTrip(t *testing.T) {
	roundTrip(t, filledGlobalBundle())
	roundTrip(t, filledRoomBundle())
}

func roundTrip[T interface {
	GlobalBundle | RoomBundle
	version() BundleVersion
}](t *testing.T, in T) {
	t.Helper()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeBundle[T](bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip changed the bundle:\n in: %+v\nout: %+v", in, out)
	}
}

func TestBundleRefusesTrailingData(t *testing.T) {
	data, err := json.Marshal(filledGlobalBundle())
	if err != nil {
		t.Fatal(err)
	}
	for _, tail := range []string{"]", "}", "{}", "x"} {
		if _, err := DecodeBundle[GlobalBundle](strings.NewReader(string(data) + tail)); err == nil {
			t.Errorf("trailing %q decoded without error", tail)
		}
	}
}

func TestBundleRefusesUnknownFields(t *testing.T) {
	data, err := json.Marshal(filledRoomBundle())
	if err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"top level": strings.Replace(string(data), `"version":1`, `"version":1,"extra":true`, 1),
		"nested":    strings.Replace(string(data), `"label":"q33"`, `"label":"q33","extra":true`, 1),
	} {
		if doc == string(data) {
			t.Fatalf("%s: the test did not change the document", name)
		}
		if _, err := DecodeBundle[RoomBundle](strings.NewReader(doc)); err == nil {
			t.Errorf("%s: unknown field decoded without error", name)
		}
	}
}

package watch

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
	"fmt"
	"os/exec"
	"os/user"
	"reflect"
	"strings"
	"testing"
)

func TestMergeFixRefusesNonOwnerBeforeGitHub(t *testing.T) {
	e, _, merged := mergeEnv(t, 1)
	t.Setenv("DYNA_MODE", "subagent")
	t.Setenv("DYNA_SESSION", e.author.ID)
	e.s.GH.run = func(context.Context, *exec.Cmd) error { t.Fatal("non-owner contacted GitHub"); return nil }
	if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err == nil || !strings.HasPrefix(err.Error(), "git pr merge-fix is owner or root only; ") {
		t.Fatalf("want formatted authority refusal, got %v", err)
	}
	if len(*merged) != 0 {
		t.Fatal("non-owner published a PR")
	}
}

func TestMergeFixPublishesOnlyRequestedPRAndLogsActor(t *testing.T) {
	for _, actor := range []string{"human", "root session ses_20261019-000001-bbbbbbbb"} {
		t.Run(actor, func(t *testing.T) {
			storetest.ClearStamp(t)
			if actor != "human" {
				t.Setenv("DYNA_MODE", "root")
				t.Setenv("DYNA_SESSION", "ses_20261019-000001-bbbbbbbb")
			}
			e, _, merged := mergeEnv(t, 1, 2)
			rewriteMergeDefault(e, func(target map[string]any) {
				target["statusCheckRollup"].(map[string]any)["state"] = "FAILURE"
			})
			if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#2"); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*merged, []int{2}) {
				t.Fatalf("repair merged %v, want [2]", *merged)
			}
			r, err := e.s.Store.Get("github.com/owner/repo#2")
			if err != nil || r.State != "merged" || r.Watching {
				t.Fatalf("repair was not reconciled: %+v %v", r, err)
			}
			owner, err := user.Current()
			if err != nil {
				t.Fatal(err)
			}
			messages, err := e.s.Notify.Rooms.Messages(e.author.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			from, human := "owner", true
			if actor != "human" {
				from, human = "merge-work:root", false
			}
			found := false
			for _, message := range messages {
				if strings.Contains(message.Text, "requested merge-fix") {
					if message.From != from || message.Human != human {
						t.Errorf("wrong audit actor: from=%q human=%v, want %q human=%v", message.From, message.Human, from, human)
					}
					if !human && message.SessionIDs[from] != "ses_20261019-000001-bbbbbbbb" {
						t.Errorf("root actor has no exact session ID: %v", message.SessionIDs)
					}
				}
				if strings.Contains(message.Text, owner.Username) && strings.Contains(message.Text, actor) &&
					strings.Contains(message.Text, "merge-fix") && strings.Contains(message.Text, r.Head) && strings.Contains(message.Text, r.URL) {
					found = true
				}
			}
			if !found {
				t.Fatalf("repair owner not logged: %+v", messages)
			}
		})
	}
}

func TestMergeFixKeepsFreshPRGates(t *testing.T) {
	for _, which := range []string{"approval", "ci", "draft", "conflict", "head", "paused"} {
		t.Run(which, func(t *testing.T) {
			e, fs, merged := mergeEnv(t, 1)
			rewriteMergeDefault(e, func(target map[string]any) {
				target["statusCheckRollup"].(map[string]any)["state"] = "FAILURE"
			})
			switch which {
			case "approval":
				if err := e.s.Store.update(func(st *State) error { st.Records["github.com/owner/repo#1"].Verdict = "changes"; return nil }); err != nil {
					t.Fatal(err)
				}
			case "ci":
				fs[1].Checks[0].Conclusion = "failure"
			case "draft":
				fs[1].Draft = true
			case "conflict":
				fs[1].Mergeability = "CONFLICTING"
			case "head":
				fs[1].Pull.Head.SHA = strings.Repeat("f", 40)
			case "paused":
				if _, err := e.s.Notify.Rooms.SetSessionsPaused(t.Context(), e.author.RoomID, true); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err == nil || !strings.Contains(err.Error(), "not eligible") {
				t.Fatalf("repair eligibility result: %v", err)
			}
			if len(*merged) != 0 {
				t.Fatalf("repair published with %s", which)
			}
		})
	}
}

func TestMergeFixRechecksAfterChildPreparation(t *testing.T) {
	e, fs, merged := mergeEnv(t, 1, 2)
	fs[2].Pull.Base.Ref = fs[1].Pull.Head.Ref
	edits := 0
	run := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), "pr edit ") {
			edits++
			if err := e.s.Store.update(func(st *State) error {
				st.Records["github.com/owner/repo#1"].Verdict = "changes"
				return nil
			}); err != nil {
				return err
			}
		}
		return run(ctx, cmd)
	}
	if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err == nil {
		t.Fatal("repair skipped final approval gate")
	}
	if edits != 2 || len(*merged) != 0 || fs[2].Pull.Base.Ref != fs[1].Pull.Head.Ref {
		t.Fatalf("repair failed to roll back preparation: merges=%v child=%s", *merged, fs[2].Pull.Base.Ref)
	}
}

func TestMergePublishersSerializeChildPreparation(t *testing.T) {
	for _, outerRepair := range []bool{false, true} {
		for _, rivalRepair := range []bool{false, true} {
			t.Run(fmt.Sprintf("outer-repair=%v/rival-repair=%v", outerRepair, rivalRepair), func(t *testing.T) {
				f := mergeTrialEnv(t, 1, 2)
				f.ci = "completed"
				e, fs, merged := f.e, f.fs, f.merged
				fs[2].Pull.Base.Ref = fs[1].Pull.Head.Ref
				if err := e.s.Store.update(func(st *State) error {
					st.Records["github.com/owner/repo#2"].BaseRef = fs[1].Pull.Head.Ref
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if !outerRepair {
					if _, err := e.s.MergeTick(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				other := *e.s
				other.Store = &Store{Root: e.s.Store.Root}
				attempted := false
				run := e.s.GH.run
				e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
					err := run(ctx, cmd)
					if strings.Contains(strings.Join(cmd.Args, " "), "pr edit ") && !attempted {
						attempted = true
						// A second service instance tries publication exactly after
						// the first retargets its child, while it still owns the lock.
						var rivalErr error
						if rivalRepair {
							rivalErr = other.MergeFix(ctx, "github.com/owner/repo#1")
						} else {
							_, rivalErr = other.MergeTick(ctx)
						}
						if rivalErr == nil || !strings.Contains(rivalErr.Error(), "publisher is active") {
							t.Errorf("overlapping publisher was not refused: %v", rivalErr)
						}
					}
					return err
				}
				var err error
				if outerRepair {
					err = e.s.MergeFix(t.Context(), "github.com/owner/repo#1")
				} else {
					_, err = e.s.MergeTick(t.Context())
				}
				if err != nil {
					t.Fatal(err)
				}
				if !attempted || !reflect.DeepEqual(*merged, []int{1}) || fs[2].Pull.Base.Ref != "main" {
					t.Fatalf("overlap corrupted preparation: attempted=%v merged=%v child=%s", attempted, *merged, fs[2].Pull.Base.Ref)
				}
			})
		}
	}
}

func TestMergeFixAuditWakeFollowsActor(t *testing.T) {
	for _, actor := range []string{"human", "root"} {
		t.Run(actor, func(t *testing.T) {
			storetest.ClearStamp(t)
			if actor == "root" {
				t.Setenv("DYNA_MODE", "root")
				t.Setenv("DYNA_SESSION", "ses_20261019-000001-bbbbbbbb")
			}
			e, _, _ := mergeEnv(t, 1)
			if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err != nil {
				t.Fatal(err)
			}
			messages, err := e.s.Notify.Rooms.Messages(e.author.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			for _, message := range messages {
				if strings.Contains(message.Text, "requested merge-fix") {
					want := actor == "human"
					if message.Wake != want || room.WakesRoot(message, "merge-work") != want {
						t.Fatalf("audit wake for %s: Wake=%v WakesRoot=%v, want %v", actor, message.Wake, room.WakesRoot(message, "merge-work"), want)
					}
					return
				}
			}
			t.Fatal("audit post was not recorded")
		})
	}
}

func TestMergeFixRefusesStampedProcessesBeforeStore(t *testing.T) {
	for _, stamp := range []struct{ key, value string }{
		{"DYNA_MODE", "subagent"},
		{"DYNA_MODE", "workflow"},
		{"DYNA_MODE", "unknown"},
		{"DYNA_SESSION", "bare-session"},
		{"DYNA_WORKER_RUN", "bare-run"},
		{"DYNA_WORKER_AGENT", "bare-worker"},
	} {
		t.Run(stamp.key+"/"+stamp.value, func(t *testing.T) {
			storetest.ClearStamp(t)
			t.Setenv(stamp.key, stamp.value)
			s := &Service{}
			err := s.MergeFix(t.Context(), "github.com/owner/repo#1")
			if err == nil || !strings.HasPrefix(err.Error(), "git pr merge-fix is owner or root only; ") {
				t.Fatalf("want formatted authority refusal before store access, got %v", err)
			}
		})
	}
}

func TestMergeFixUsesCallerContext(t *testing.T) {
	t.Run("session cannot borrow the engine owner", func(t *testing.T) {
		storetest.ClearStamp(t)
		s := &Service{}
		ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: "caller"})
		if err := s.MergeFix(ctx, "github.com/owner/repo#1"); err == nil || !strings.HasPrefix(err.Error(), "git pr merge-fix is owner or root only; ") {
			t.Fatalf("caller refusal = %v", err)
		}
	})
	t.Run("root caller keeps its session", func(t *testing.T) {
		storetest.ClearStamp(t)
		e, _, merged := mergeEnv(t, 1)
		const session = "ses_20261019-000001-bbbbbbbb"
		ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Root, Session: session, Room: e.author.RoomID})
		if err := e.s.MergeFix(ctx, "github.com/owner/repo#1"); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(*merged, []int{1}) {
			t.Fatalf("merged = %v", *merged)
		}
		messages, err := e.s.Notify.Rooms.Messages(e.author.RoomID)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range messages {
			if strings.Contains(message.Text, "requested merge-fix") {
				if message.Human || message.Wake || message.SessionIDs[message.From] != session || !strings.Contains(message.Text, "root session "+session) {
					t.Fatalf("audit lost caller: %+v", message)
				}
				return
			}
		}
		t.Fatal("merge-fix audit is missing")
	})
}

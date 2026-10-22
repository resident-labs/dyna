package pr

import (
	"bytes"
	"dyna-agent/internal/authority"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	prview "dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func TestVerdictLifecycleCLI(t *testing.T) {
	const (
		url      = "https://github.com/owner/repo/pull/17"
		key      = "github.com/owner/repo#17"
		head     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		newHead  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		reviewer = "ses_20261017-000001-aaaaaaaa"
		other    = "ses_20261017-000002-bbbbbbbb"
	)
	// All cases exercise the same engine boundary. Reset PR state per case,
	// while keeping database migrations and socket setup outside the table.
	storetest.Isolate(t)
	rec, err := room.DefaultStore().Create(room.CreateInput{Name: "verdict"})
	if err != nil {
		t.Fatal(err)
	}
	s := watch.New()
	view := &prview.Service{Notify: s.Notify}
	s.Store = view.Watcher().Store
	calls, err := websvc.Bind([]websvc.Binding{{Name: "RecordPRVerdict", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.RecordPRVerdict}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	// These stale and paused cases must decide without a GitHub request.
	t.Setenv(ghclient.BinaryEnv, "/must-not-contact-github")
	for _, tc := range []struct {
		name   string
		merged bool
		actor  string
		github bool
		paused bool
	}{
		{name: "assigned after merge", merged: true, actor: reviewer},
		{name: "other after merge", merged: true, actor: other},
		{name: "assigned after push", actor: reviewer},
		{name: "other after push", actor: other},
		{name: "GitHub review already posted", actor: reviewer, github: true},
		{name: "assigned while watcher paused", actor: reviewer, paused: true},
		{name: "other while watcher paused", actor: other, paused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &watch.Record{
				Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, URL: url,
				Author: "author", RoomID: rec.ID, State: "open", Watching: true,
				Head: newHead, RoundHead: newHead, Round: 2, Reviewer: reviewer,
				LastReviewer: reviewer, LastReviewHead: head, ReviewerMode: "session",
				ReviewPublishing: watch.PublishingLocal,
			}
			if tc.merged {
				r.State, r.Watching, r.Reviewer, r.RoundHead = "merged", false, "", ""
				r.Head = head
			}
			if tc.github {
				r.ReviewPublishing = watch.PublishingGitHub
			}
			state := watch.State{Version: 1, Records: map[string]*watch.Record{key: r}, Reviewers: map[string]watch.Reviewer{}}
			if tc.paused {
				r.Head, r.RoundHead = head, head
				r.QuotaAccount = "fixture"
				scope := watch.RateLimitScope{Host: r.Host, Account: r.QuotaAccount, Resource: "core"}
				state.RateLimits = map[string]watch.RateLimit{scope.Key(): {PauseUntil: time.Now().Add(time.Hour)}}
			}
			before, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			saveState(t, s.Store.Root, before)
			revision := storeRevision(t, s.Store.Root)
			cmd := Command(Options{Service: func() *watch.Service { return s }})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			t.Setenv(mode.EnvMode, mode.Subagent)
			t.Setenv(mode.EnvSession, tc.actor)
			t.Setenv(mode.EnvRoomID, rec.ID)
			args := []string{"verdict", url, "approve", "Review complete.", "--head", head}
			if tc.github {
				args = append(args, "--review-id", "987654")
			}
			cmd.SetArgs(args)
			err = cmd.Execute()
			if tc.actor != reviewer {
				if err == nil {
					t.Fatalf("unassigned reviewer succeeded: %s", out.String())
				}
			} else {
				if err != nil {
					t.Fatalf("stale verdict failed: %v; output: %s", err, out.String())
				}
				if tc.paused {
					if out.String() != "verdict saved; applies after GitHub refresh\n" {
						t.Fatalf("pending verdict output = %q", out.String())
					}
					saved, err := s.Store.Get(key)
					if err != nil || saved.PendingVerdict == nil || saved.Verdict != "" {
						t.Fatalf("pending verdict changed readiness or sent notices: %+v, %v", saved, err)
					}
					messages, err := s.Notify.Rooms.Messages(rec.ID)
					if err != nil || len(messages) != 0 {
						t.Fatalf("pending verdict posted notices: %+v, %v", messages, err)
					}
					return
				}
				want := "newer head"
				if tc.merged {
					want = "merged"
				}
				if !strings.Contains(out.String(), want) || strings.Count(out.String(), "\n") != 1 {
					t.Fatalf("expected one stale message containing %q: %q", want, out.String())
				}
				if tc.github && !strings.Contains(out.String(), "GitHub") {
					t.Fatalf("missing notice that the posted review stays on GitHub: %s", out.String())
				}
			}
			messages, err := s.Notify.Rooms.Messages(rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if storeRevision(t, s.Store.Root) != revision || len(messages) != 0 {
				t.Fatalf("stale or rejected verdict changed state or posted %+v", messages)
			}
		})
	}
}

func TestNamedReviewerVerdictCommandNeedsNoPoolAssignment(t *testing.T) {
	storetest.Isolate(t)
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "named-cli"})
	if err != nil {
		t.Fatal(err)
	}
	mgr := subagent.NewManager()
	svc := &prview.Service{Notify: notify.New(rooms, mgr)}
	s := svc.Watcher()
	const reviewer = "ses_20261017-000005-eeeeeeee"
	const key = "github.com/owner/repo#17"
	recordtest.Write(t, mgr.Store.Root, reviewer, subagent.Meta{ID: reviewer, Name: "arch-review", RoomID: rec.ID, Room: rec.Name})
	st, _ := s.Store.Read()
	st.Records[key] = &watch.Record{Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, RoomID: rec.ID, Kind: "github", State: "open", Watching: true, Author: "author", Head: "head", QuotaAccount: "fixture"}
	scope := watch.RateLimitScope{Host: "github.com", Account: "fixture", Resource: "core"}
	st.RateLimits = map[string]watch.RateLimit{scope.Key(): {PauseUntil: time.Now().Add(time.Hour)}}
	data, _ := json.Marshal(st)
	saveState(t, s.Store.Root, data)
	policy, err := s.RoomChangePolicy(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy.Review, policy.Reviewers = "off", []string{reviewer}
	owner := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	if err = svc.SetRoomChangePolicy(owner, rec.ID, policy); err != nil {
		t.Fatal(err)
	}
	bindings, err := websvc.Bind([]websvc.Binding{{Name: "RecordPRVerdict", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: svc.RecordPRVerdict}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), bindings)
	t.Setenv("DYNA_MODE", "subagent")
	t.Setenv("DYNA_SESSION", reviewer)
	t.Setenv("DYNA_ROOM_ID", rec.ID)
	cmd := Command(Options{Service: func() *watch.Service { return s }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("Reviewed exact head"))
	cmd.SetArgs([]string{"verdict", "https://github.com/owner/repo/pull/17", "approve", "--body-file", "-", "--head", "head"})
	if err = cmd.Execute(); err != nil {
		t.Fatalf("named verdict CLI = %s %v", out.String(), err)
	}
	saved, err := s.Store.Get(key)
	if err != nil || saved.Reviewer != "" || saved.PendingVerdict == nil || saved.PendingVerdict.Actor != reviewer {
		t.Fatalf("CLI lost its caller or created an assignment: %+v, %v", saved, err)
	}
}

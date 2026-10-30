package pr

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func TestAgentWriteParity(t *testing.T) {
	storetest.Isolate(t)
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "parity"})
	if err != nil {
		t.Fatal(err)
	}
	roomfixture.Repository(t, rooms, rec.ID, "github.com", "owner/repo", "read-write")
	if err := rooms.SetAttribution(rec.ID, "", room.Attribution{Text: "Agent attribution", Placement: room.AttributionLine}); err != nil {
		t.Fatal(err)
	}
	s := watch.New()
	s.Notify = notify.New(rooms, subagent.NewManager())
	const url = "https://github.com/owner/repo/pull/17"
	const key = "github.com/owner/repo#17"
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	state := watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, URL: url, RoomID: rec.ID, Head: head, State: "open", Watching: true}}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	saveState(t, s.Store.Root, raw)
	if err := s.ConfigurePublishing(rec.ID, watch.PublishingGitHub); err != nil {
		t.Fatal(err)
	}
	var requests []approvals.RequestInput
	var actorMode string
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "ApprovalRequest", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, in approvals.RequestInput) (approvals.Request, error) {
			requests = append(requests, in)
			return approvals.Request{ID: "A-1"}, nil
		}},
		{Name: "ApprovalWait", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, _, id string, _ time.Duration) (approvals.Request, error) {
			if actorMode == mode.Subagent {
				return approvals.Request{ID: id, State: approvals.StateDenied, Result: &approvals.Result{Actor: "parent", Reason: "denied"}}, nil
			}
			code := 0
			return approvals.Request{ID: id, State: approvals.StateDone, Result: &approvals.Result{ExitCode: &code}}, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	binary := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ghclient.BinaryEnv, binary)
	t.Setenv(ghclient.ReadOnlyEnv, string(ghclient.GuardWorker))
	t.Setenv(mode.EnvRoomID, rec.ID)
	t.Setenv(mode.EnvSession, "ses_20261029-000001-aaaaaaaa")
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("feedback"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		cli, raw []string
	}{
		{"comment", []string{"comment", url, "feedback"}, []string{"pr", "comment", url, "--body", "feedback"}},
		{"review", []string{"review", url, "--body-file", bodyFile}, ghclient.PinnedReviewArgs("github.com", "owner/repo", 17, head, "feedback")},
		{"edit", []string{"edit", url, "--body-file", bodyFile}, []string{"pr", "edit", url, "--body", "feedback"}},
	}
	for _, actor := range []string{mode.Root, mode.Subagent} {
		actorMode = actor
		t.Setenv(mode.EnvMode, actor)
		for _, tc := range cases {
			t.Run(actor+"/"+tc.name, func(t *testing.T) {
				requests = nil
				rawErr := ghclient.SessionCommand(context.Background(), tc.raw)
				cmd := Command(Options{Service: func() *watch.Service { return s }})
				cmd.SetOut(&bytes.Buffer{})
				cmd.SetErr(&bytes.Buffer{})
				cmd.SetArgs(tc.cli)
				cliErr := cmd.Execute()
				if (rawErr == nil) != (cliErr == nil) || (rawErr != nil && rawErr.Error() != cliErr.Error()) {
					t.Fatalf("raw decision %v != CLI decision %v", rawErr, cliErr)
				}
				if len(requests) != 2 {
					t.Fatalf("requests = %d", len(requests))
				}
				for i := range requests {
					for _, file := range requests[i].Files {
						for n, arg := range requests[i].Argv {
							requests[i].Argv[n] = strings.ReplaceAll(arg, file.Path, string(file.Data))
						}
					}
				}
				if !reflect.DeepEqual(requests[0].Argv, requests[1].Argv) {
					t.Fatalf("raw argv %q != CLI argv %q", requests[0].Argv, requests[1].Argv)
				}
			})
		}
	}
}

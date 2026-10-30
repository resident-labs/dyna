package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

type fakeClient struct {
	host     contract.HostAddRequest
	room     contract.RoomCreateRequest
	repoAdd  contract.RoomRepoAddRequest
	repoList contract.RoomReposRequest
	start    contract.SessionStartRequest
	close    contract.SessionCloseRequest
	post     contract.PostRequest
	events   []contract.Event
	after    int64
}

func (f *fakeClient) HostAdd(_ context.Context, q contract.HostAddRequest) (contract.HostAddResponse, error) {
	f.host = q
	return contract.HostAddResponse{}, nil
}
func (f *fakeClient) RoomCreate(_ context.Context, q contract.RoomCreateRequest) (contract.RoomCreateResponse, error) {
	f.room = q
	return contract.RoomCreateResponse{Room: "room_1"}, nil
}
func (f *fakeClient) RoomRepoAdd(_ context.Context, q contract.RoomRepoAddRequest) (contract.RoomRepoAddResponse, error) {
	f.repoAdd = q
	return contract.RoomRepoAddResponse{}, nil
}
func (f *fakeClient) RoomRepos(_ context.Context, q contract.RoomReposRequest) (contract.RoomReposResponse, error) {
	f.repoList = q
	return contract.RoomReposResponse{Repos: []contract.RoomRepo{{Repo: "owner/a", Default: true}, {Repo: "owner/b"}}}, nil
}
func (f *fakeClient) SessionStart(_ context.Context, q contract.SessionStartRequest) (contract.SessionStartResponse, error) {
	f.start = q
	return contract.SessionStartResponse{Session: "ses_1"}, nil
}
func (f *fakeClient) SessionClose(_ context.Context, q contract.SessionCloseRequest) (contract.SessionCloseResponse, error) {
	f.close = q
	return contract.SessionCloseResponse{}, nil
}
func (f *fakeClient) Post(_ context.Context, q contract.PostRequest) (contract.PostResponse, error) {
	f.post = q
	return contract.PostResponse{Event: "ev_1"}, nil
}
func (f *fakeClient) EventsList(_ context.Context, q contract.EventsListRequest) (contract.EventsListResponse, error) {
	f.after = q.AfterSeq
	return contract.EventsListResponse{Events: f.events}, nil
}
func (f *fakeClient) Events(_ context.Context, _ contract.RoomID, after int64, emit func(contract.Event) error) error {
	f.after = after
	return emit(contract.Event{Seq: 3, Body: contract.PostCreated{Text: "third"}})
}
func runCLI(t *testing.T, f *fakeClient, args ...string) string {
	t.Helper()
	root := &cobra.Command{Use: "dyna", SilenceUsage: true, SilenceErrors: true}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	addCLI(root, func() client { return f })
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}
func TestHostAddMapsFlags(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "host", "add", "h", "--ssh", "u@h", "--owner-only-admin")
	if f.host.Name != "h" || f.host.SSH != "u@h" || !f.host.OwnerOnlyAdmin {
		t.Fatalf("%+v", f.host)
	}
}
func TestRoomCreateMapsName(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "room", "create", "work", "--root-profile", "p")
	if f.room.Name != "work" || f.room.RootProfile != "p" {
		t.Fatalf("%+v", f.room)
	}
}
func TestRoomRepos(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "room", "repo", "add", "r", "owner/a", "--default")
	if f.repoAdd.Room != "r" || f.repoAdd.Repo != "owner/a" || !f.repoAdd.Default {
		t.Fatalf("%+v", f.repoAdd)
	}
	out := runCLI(t, f, "room", "repo", "list", "r")
	if f.repoList.Room != "r" || out != "owner/a (default)\nowner/b\n" {
		t.Fatalf("%+v: %q", f.repoList, out)
	}
}
func TestSessionStartMapsFlags(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "session", "start", "--room", "r", "--name", "n", "--profile", "p", "--goal", "g", "--brief", "b", "--repo", "owner/name", "--parent", "s")
	if f.start.Room != "r" || f.start.Name != "n" || f.start.Profile != "p" || f.start.Goal != "g" || f.start.Brief != "b" || f.start.Repo != "owner/name" || f.start.Parent != "s" {
		t.Fatalf("%+v", f.start)
	}
}
func TestSessionCloseDiscard(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "session", "close", "s", "--discard")
	if f.close.Session != "s" || !f.close.Discard {
		t.Fatalf("%+v", f.close)
	}
}

func TestSessionCloseMapsID(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "session", "close", "s")
	if f.close.Session != "s" {
		t.Fatalf("%+v", f.close)
	}
}
func TestPostMapsFlags(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "post", "hello", "--room", "r", "--mention", "bob")
	if f.post.Room != "r" || f.post.Text != "hello" || len(f.post.Mentions) != 1 || f.post.Mentions[0] != "bob" {
		t.Fatalf("%+v", f.post)
	}
}
func TestRoomLogFollowsInOrder(t *testing.T) {
	f := &fakeClient{events: []contract.Event{{Seq: 1, Body: contract.PostCreated{Text: "first"}}, {Seq: 2, Body: contract.PostCreated{Text: "second"}}}}
	out := runCLI(t, f, "room", "log", "r", "--follow")
	if f.after != 2 || !(strings.Index(out, "first") < strings.Index(out, "second") && strings.Index(out, "second") < strings.Index(out, "third")) {
		t.Fatalf("after=%d output=%s", f.after, out)
	}
}

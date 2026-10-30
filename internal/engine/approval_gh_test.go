package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestApprovedCommentRecordsItsID(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'https://github.com/owner/repo/pull/7#issuecomment-4242'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ghclient.BinaryEnv, fake)
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "comment-room"})
	if err != nil {
		t.Fatal(err)
	}
	roomfixture.Repository(t, rooms, rec.ID, "github.com", "owner/repo", "read-write")
	raw, err := json.Marshal(approvals.GHPayload{Argv: []string{"pr", "comment", "https://github.com/owner/repo/pull/7", "--body", "hi"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	const requester = "ses_20261029-000001-aaaaaaaa"
	var claimed []int64
	var session string
	claim := func(s string, id int64) error {
		session = s
		claimed = append(claimed, id)
		return nil
	}
	_, err = ghApprovalExecutor(rooms, claim)(context.Background(), approvals.Request{Room: rec.ID, Requester: requester, Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0] != 4242 || session != requester {
		t.Fatalf("claimed %v for %q, want comment 4242 for %q", claimed, session, requester)
	}
}

func TestBindApprovalMediaVerifiesHash(t *testing.T) {
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "media-test"})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("approved media")
	attachment, err := rooms.PutAttachment(rec.ID, "image.png", data)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := rooms.Post(rec.ID, room.PostInput{From: "media-test", To: []string{"media-test"}, Text: "media", Attachments: []room.Attachment{attachment}})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	payload := approvals.GHPayload{Argv: []string{"pr", "comment", "7", "--attach", "image.png"}, Media: []approvals.Media{{Path: "image.png", Name: "image.png", PostID: msg.ID, AttachmentID: attachment.ID, SHA256: fmt.Sprintf("%x", sum)}}}
	argv, cleanup, err := bindApprovalFilesForRoom(payload, rooms, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !strings.HasSuffix(argv[4], ".png") {
		t.Fatalf("extension lost: %s", argv[4])
	}
	restored, err := os.ReadFile(argv[4])
	if err != nil || string(restored) != string(data) {
		t.Fatalf("restored %q: %v", restored, err)
	}
	payload.Media[0].SHA256 = strings.Repeat("0", 64)
	if _, _, err := bindApprovalFilesForRoom(payload, rooms, rec.ID); err == nil {
		t.Fatal("accepted altered hash")
	}
}

func TestRootMediaApprovalChecksItsAttachment(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "root-media"})
	if err != nil {
		t.Fatal(err)
	}
	root := subagent.Meta{ID: "ses_20261020-000000-abcde111", Name: "root", Room: rec.Name, RoomID: rec.ID, Role: subagent.RoleRoot}
	if err := sessionlife.Save(s.rooms.Notify.Sessions.Store.Root, root.ID, root); err != nil {
		t.Fatal(err)
	}
	actor := authority.Actor{Role: authority.Root, Session: root.ID, Room: rec.ID}
	in := approvals.RequestInput{Argv: []string{"pr", "comment", "7", "--attach", "image.png"}, Media: []approvals.Media{{Path: "image.png", Name: "image.png", Data: []byte("bad"), SHA256: strings.Repeat("0", 64)}}}
	_, err = s.approvalRequest(authority.WithActor(t.Context(), actor), in)
	if err == nil || !strings.Contains(err.Error(), "media hash mismatch") {
		t.Fatalf("root media must reach hash validation; got %v", err)
	}
}

func TestMediaWritesRequireAgentApproval(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	rec, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "media-routing"})
	if err != nil {
		t.Fatal(err)
	}
	roomfixture.Repository(t, s.rooms.Notify.Rooms, rec.ID, "github.com", "owner/repo", "read-write")
	root := subagent.Meta{ID: "ses_20261020-000000-abcde111", Name: "root", Room: rec.Name, RoomID: rec.ID, Role: subagent.RoleRoot}
	child := subagent.Meta{ID: "ses_20261020-000000-abcde222", Name: "child", Room: rec.Name, RoomID: rec.ID, ParentSession: root.ID}
	for _, m := range []subagent.Meta{root, child} {
		if err := sessionlife.Save(s.rooms.Notify.Sessions.Store.Root, m.ID, m); err != nil {
			t.Fatal(err)
		}
	}
	fake := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"writer"}]}}';;
"auth token") echo token;;
"pr view") echo '{"number":7,"url":"https://github.com/owner/repo/pull/7","headRepository":{"name":"repo"},"headRepositoryOwner":{"login":"owner"}}';;
"api --hostname") echo '{"full_name":"owner/repo","private":false,"visibility":"public","permissions":{"push":true}}';;
esac
`
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ghclient.BinaryEnv, fake)
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	media := approvals.Media{Path: "image.png", Name: "image.png", Data: data, SHA256: fmt.Sprintf("%x", sum)}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/owner/repo.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	actor := authority.Actor{Role: authority.Session, Session: child.ID, Room: rec.ID}
	for _, args := range [][]string{{"pr", "comment", "https://github.com/owner/repo/pull/7", "--attach", "image.png"}} {
		in := approvals.RequestInput{Argv: args, Cwd: dir, Media: []approvals.Media{media}}
		req, err := s.approvalRequest(authority.WithActor(t.Context(), actor), in)
		if err != nil {
			t.Fatal(err)
		}
		if req.Class != approvals.ClassAgent || req.State != approvals.StatePending || req.At != root.Name {
			t.Fatalf("%v: class %s state %s at %s; want agent pending at root", args, req.Class, req.State, req.At)
		}
		if !strings.Contains(req.Body, "Media destination:") {
			t.Fatal("missing media destination")
		}
	}
	const key = "github.com/owner/repo#7"
	if err := prstate.UpdateRecords(t.Context(), s.rooms.PRs.Watcher().Store.Root, []string{key}, func(st *prstate.State) error {
		st.Records[key] = &prstate.Record{Key: key, Host: "github.com", Repo: "owner/repo", Number: 7, RoomID: rec.ID, Author: child.ID, State: prstate.PROpen, Watching: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mediaRequest, err := s.approvalRequest(authority.WithActor(t.Context(), actor), approvals.RequestInput{Argv: []string{"pr", "comment", "7", "--repo", "owner/repo", "--body", "hello", "--attach", "image.png"}, Cwd: dir, Repo: "owner/repo", Media: []approvals.Media{media}})
	if err != nil || mediaRequest.State != approvals.StatePending || mediaRequest.At != root.Name || mediaRequest.Class != approvals.ClassAgent {
		t.Fatalf("own PR media = %+v, %v", mediaRequest, err)
	}
	before, err := s.rooms.Notify.Rooms.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		actor authority.Actor
		args  []string
	}{
		{authority.Actor{Role: authority.Root, Session: root.ID, Room: rec.ID}, []string{"issue", "create", "--repo", "owner/repo", "--title", "title", "--body", "body"}},
		{actor, []string{"pr", "edit", "7", "--repo", "owner/repo", "--title", "title"}},
	} {
		result, err := s.approvalRequest(authority.WithActor(t.Context(), tc.actor), approvals.RequestInput{Argv: tc.args, Cwd: dir, Repo: "owner/repo"})
		if err != nil || result.ID != "" || result.State != approvals.StateDone {
			t.Fatalf("inline result = %+v, %v", result, err)
		}
	}
	after, err := s.rooms.Notify.Rooms.Messages(rec.ID)
	if err != nil || len(after) != len(before) {
		t.Fatalf("inline messages = %d before, %d after, %v", len(before), len(after), err)
	}
}

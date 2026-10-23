package roomsvc

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

type checkedTaskSpawner struct {
	before func()
	next   subagent.Spawner
}

func (s checkedTaskSpawner) Spawn(m *subagent.Manager, id string, turn int, lock *os.File) error {
	s.before()
	return s.next.Spawn(m, id, turn, lock)
}

func TestSpawnWithImageSavesPostBeforeStarting(t *testing.T) {
	e := newEnv(t)
	t.Setenv("HOME", t.TempDir())
	rec := e.repoRoom("images")
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aWQAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := e.svc.Attach(rec.ID, "phone screenshot.png", png)
	if err != nil {
		t.Fatal(err)
	}
	// Ignore caller-supplied type and size; the normal post rebuilds them.
	attachment.MIME = "text/plain"
	attachment.Size = 999
	task := "RESPOND: my brother is asking about this"
	var request room.Message
	e.mgr.Spawner = checkedTaskSpawner{next: e.mgr.Spawner, before: func() {
		posts := e.posts(rec.ID)
		if len(posts) != 1 {
			t.Fatalf("posts before launch: %+v", posts)
		}
		request = posts[0]
		if request.Kind != "message" || request.Text != task || request.From != rec.Name || !request.Human || len(request.To) != 1 || request.To[0] != rec.Name || len(request.Attachments) != 1 || !request.Attachments[0].Image() {
			t.Fatalf("saved request: %+v", request)
		}
	}}
	result, err := e.svc.Send(e.ctx, rec.ID, "/mock "+task, "", "", attachment)
	if err != nil || result.Kind != "spawn" || result.Spawn == nil {
		t.Fatalf("send with image: %+v %v", result, err)
	}
	info := e.wait(result.Spawn.Member.ID, 1)
	turn, err := e.svc.sessions().ReadTurn(info.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	path, err := e.svc.rooms().AttachmentPath(rec.ID, attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) || !strings.HasSuffix(path, ".png") || !strings.Contains(turn.Prompt, path) || !strings.Contains(turn.Prompt, attachment.Name) || !strings.Contains(turn.Prompt, task) || !strings.Contains(turn.Prompt, "tool that reads image files") {
		t.Fatalf("first prompt: %s\nimage path: %s", turn.Prompt, path)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, png) {
		t.Fatalf("agent image bytes: %v", err)
	}
	var requests, spawnRecords int
	for _, p := range e.posts(rec.ID) {
		if p.ID == request.ID {
			requests++
		}
		if p.Kind == "session" && p.Notice.Kind == "spawned" {
			spawnRecords++
		}
	}
	if requests != 1 || spawnRecords != 1 || len(turn.Steering) != 0 {
		t.Fatalf("duplicate delivery: requests=%d spawns=%d steering=%+v", requests, spawnRecords, turn.Steering)
	}
	feed, err := e.svc.FeedTail(rec.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range feed.Posts {
		if p.ID == request.ID {
			found = p.Owner && p.Human && len(p.Attachments) == 1
		}
	}
	if !found {
		t.Fatal("owner file post missing from feed")
	}
}

func TestSpawnWithFilesStillRefusesReply(t *testing.T) {
	e := newEnv(t)
	rec := e.repoRoom("images")
	attachment, err := e.svc.Attach(rec.ID, "note.txt", []byte("attachment"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Send(e.ctx, rec.ID, "/mock RESPOND: ready", "msg_reply", "", attachment)
	if err == nil || !strings.Contains(err.Error(), "cannot start as a reply") {
		t.Fatalf("spawn reply: %v", err)
	}
	if posts := e.posts(rec.ID); len(posts) != 0 {
		t.Fatalf("reply refusal wrote posts: %+v", posts)
	}
}

func TestSpawnFailurePreservesFilePost(t *testing.T) {
	e := newEnv(t)
	rec := e.repoRoom("files")
	attachment, err := e.svc.Attach(rec.ID, "note.txt", []byte("attachment"))
	if err != nil {
		t.Fatal(err)
	}
	// Preview succeeds, but a launch-time profile check fails after the post.
	original := e.mgr.Profiles
	calls := 0
	e.mgr.Profiles = func() (*profile.Store, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("profile store unavailable")
		}
		return original()
	}
	_, err = e.svc.Send(e.ctx, rec.ID, "/mock RESPOND: ready", "", "", attachment)
	if err == nil || !strings.Contains(err.Error(), "task and files were saved as post") {
		t.Fatalf("failed launch: %v", err)
	}
	posts := e.posts(rec.ID)
	if len(posts) != 1 || len(posts[0].Attachments) != 1 || posts[0].Kind != "message" {
		t.Fatalf("saved files after failure: %+v", posts)
	}
}

package subagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

func TestReopenPreservesConversationAndCanResume(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("RESPOND: first SLEEP:1", func(o *StartOptions) { o.Name = "reopen-me" })
	info, _ = e.wait(info.ID, 1)
	before, _ := e.m.Store.ReadTurn(info.ID, 1)
	closed, err := e.m.Close(e.ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := e.m.Reopen(context.Background(), info.Address())
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID != info.ID || reopened.Name != info.Name || reopened.RoomID != info.RoomID || reopened.Profile != info.Profile || reopened.HarnessSession != info.HarnessSession || reopened.Turns != info.Turns || !reopened.Finished.IsZero() || reopened.Status != StatusIdle {
		t.Fatalf("changed conversation: before %+v after %+v closed %+v", info, reopened, closed)
	}
	after, _ := e.m.Store.ReadTurn(info.ID, 1)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reopen changed turn history")
	}
	if _, err := e.send(info.ID, "RESPOND: second SLEEP:1", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	_, turn := e.wait(info.ID, 2)
	if turn.Status != TurnOK {
		t.Fatalf("resume failed: %+v", turn)
	}
	reopened, _ = e.m.Store.Info(info.ID)
	if reopened.Status == StatusClosed {
		t.Fatal("old finish closed reopened session")
	}
}

func TestReopenRefusesNameConflictAndMissingHistory(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("RESPOND: old SLEEP:1", func(o *StartOptions) { o.Name = "same-name" })
	info, _ = e.wait(info.ID, 1)
	if _, err := e.m.Close(e.ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	replacement, _ := e.start("RESPOND: new SLEEP:1", func(o *StartOptions) { o.Name = info.Name })
	replacement, _ = e.wait(replacement.ID, 1)
	for _, ref := range []string{info.ID, info.Address()} {
		if _, err := e.m.Reopen(context.Background(), ref); err == nil || !strings.Contains(err.Error(), replacement.ID) {
			t.Fatalf("conflict %s: %v", ref, err)
		}
	}
	if _, err := e.m.Close(e.ctx, replacement.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.updateMetaContext(e.ctx, info.ID, func(meta *Meta) { meta.Resumable = false }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Reopen(context.Background(), info.ID); err == nil || !strings.Contains(err.Error(), "not resumable") {
		t.Fatalf("missing resume guard: %v", err)
	}
	latest, err := e.m.Reopen(context.Background(), info.Address())
	if err != nil || latest.ID != replacement.ID {
		t.Fatalf("latest closed address: %+v %v", latest, err)
	}
}

func TestExactRecipientSurvivesReopenWithoutClockAmbiguity(t *testing.T) {
	meta := Meta{ID: "original", Room: "room", Name: "worker", ClosedPosts: map[string]bool{"legacy-replacement": true}}
	posts := []room.Message{
		{ID: "original-work", From: "room", To: []string{meta.Address()}, SessionIDs: map[string]string{meta.Address(): meta.ID}},
		{ID: "replacement-work", From: "room", To: []string{meta.Address()}, SessionIDs: map[string]string{meta.Address(): "replacement"}},
		{ID: "legacy-replacement", From: "room", To: []string{meta.Address()}},
	}
	got := forSession(nil, meta, posts)
	if len(got) != 1 || got[0].ID != "original-work" {
		t.Fatalf("session IDs lost recipient identity: %+v", got)
	}
}

func TestReopenNameHeldOnlyByOpenOrFinishedSessions(t *testing.T) {
	for _, state := range []string{"consumed", "finished"} {
		t.Run(state, func(t *testing.T) {
			e := newTestEnv(t)
			e.joinRunners()
			original, _ := e.start("SLEEP:1 RESPOND: original", func(o *StartOptions) { o.Name = "reused" })
			original, _ = e.wait(original.ID, 1)
			if _, err := e.m.Close(e.ctx, original.ID); err != nil {
				t.Fatal(err)
			}
			replacement, _ := e.start("SLEEP:1 RESPOND: replacement", func(o *StartOptions) { o.Name = original.Name })
			replacement, _ = e.wait(replacement.ID, 1)
			if state == "consumed" {
				if _, err := e.m.Close(e.ctx, replacement.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := e.m.Store.UpdateMeta(replacement.ID, func(meta *Meta) {
				if state == "finished" {
					meta.Finished = time.Now()
					meta.Status = StatusDormant
				} else {
					meta.ConsumedBy = "successor"
				}
			}); err != nil {
				t.Fatal(err)
			}
			_, err := e.m.Reopen(context.Background(), original.ID)
			if state == "finished" && (err == nil || !strings.Contains(err.Error(), replacement.ID)) {
				t.Fatalf("finished session did not hold name: %v", err)
			}
			if state == "consumed" && err != nil {
				t.Fatalf("consumed session held name: %v", err)
			}
		})
	}
}

func TestTurnBoundaryPrunesClosedPosts(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: original")
	info, _ = e.wait(info.ID, 1)
	post, err := e.m.rooms().Post(info.RoomID, room.PostInput{From: info.Room, To: []string{info.Address()}, Text: "excluded"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.ClosedPosts = map[string]bool{post.ID: true} }); err != nil {
		t.Fatal(err)
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	e.m.pruneClosedPosts(&meta)
	if !meta.ClosedPosts[post.ID] {
		t.Fatal("pruned exclusion ahead of cursor")
	}
	if _, err := e.send(info.ID, "SLEEP:1 RESPOND: next", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	_, turn := e.wait(info.ID, 2)
	if strings.Contains(turn.Prompt, "excluded") {
		t.Fatal("excluded post reached prompt")
	}
	meta, _ = e.m.Store.ReadMeta(info.ID)
	if len(meta.ClosedPosts) != 0 {
		t.Fatalf("exclusions behind cursor remain: %+v", meta.ClosedPosts)
	}
}

func TestPruneClosedPostsPreservesExclusionsBeyondFirstPage(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: original")
	info, _ = e.wait(info.ID, 1)
	meta := info.Meta
	meta.ClosedPosts = map[string]bool{"late-exclusion": true}
	path := filepath.Join(e.m.rooms().Root, meta.RoomID, "messages.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// ReadFrom limits each page to 4 MiB. Invalid filler records are skipped.
	filler := strings.Repeat(strings.Repeat("x", 512<<10)+"\n", 9)
	late, _ := json.Marshal(room.Message{ID: "late-exclusion"})
	if _, err := f.WriteString(filler + string(late) + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	e.m.pruneClosedPosts(&meta)
	if !meta.ClosedPosts["late-exclusion"] {
		t.Fatal("pruned exclusion beyond first room page")
	}
}

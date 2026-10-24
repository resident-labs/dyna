package roomsvc

import (
	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"dyna-agent/internal/subagent"
)

func seedTurnRow(t testing.TB, s *subagent.Store, id string, n int, raw []byte) {
	t.Helper()
	db, err := database.Open(filepath.Dir(s.Root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO session_turns(session,n,revision,turn) VALUES (?,?,(SELECT COALESCE(MAX(revision),0)+1 FROM session_turns),?) ON CONFLICT(session,n) DO UPDATE SET revision=excluded.revision,turn=excluded.turn", id, n, raw); err != nil {
		t.Fatal(err)
	}
	// Fixture JSON contains inline prompt text, so discard the replaced references.
	if _, err := db.Exec("DELETE FROM prompt_refs WHERE session=? AND n=?", id, n); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptDefaults(t *testing.T) {
	e := newEnv(t)
	lead := e.start(e.room("transcript-default"), "lead", nil)
	for n := 2; n <= 8; n++ {
		data, err := json.Marshal(subagent.Turn{N: n, Status: subagent.TurnOK})
		if err != nil {
			t.Fatal(err)
		}
		seedTurnRow(t, e.mgr.Store, lead.ID, n, data)
	}
	if _, err := e.mgr.Store.UpdateMeta(lead.ID, func(meta *subagent.Meta) { meta.Turns = 8 }); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Transcript(lead.ID)
	if err != nil || len(got.Turns) != 6 || got.Turns[0].N != 3 {
		t.Fatalf("default transcript: %+v, %v", got, err)
	}
	for _, count := range []int{-1, 0, 1, 6, 8} {
		got, err := e.svc.TranscriptMore(lead.ID, count)
		want := count
		if want < 6 {
			want = 6
		}
		if err != nil || len(got.Turns) != want {
			t.Fatalf("count %d: %d turns, %v", count, len(got.Turns), err)
		}
	}
}

// The web needs the stored wrap-up type so it can hide the engine's prompt.
func TestTranscriptMarksWrapUp(t *testing.T) {
	e := newEnv(t)
	lead := e.start(e.room("wrap-up-view"), "lead", nil)
	data, err := json.Marshal(subagent.Turn{N: 1, Status: subagent.TurnOK, WrapUp: true, Prompt: "[DYNA WRAP-UP] check open questions"})
	if err != nil {
		t.Fatal(err)
	}
	seedTurnRow(t, e.mgr.Store, lead.ID, 1, data)
	if _, err := e.mgr.Store.UpdateMeta(lead.ID, func(meta *subagent.Meta) { meta.Turns = 1 }); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Transcript(lead.ID)
	if err != nil || len(got.Turns) != 1 {
		t.Fatalf("transcript: %+v, %v", got, err)
	}
	raw, err := json.Marshal(got.Turns[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		WrapUp bool `json:"wrapUp"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if !wire.WrapUp {
		t.Fatalf("turn lost its wrap-up type: %s", raw)
	}
}

func TestAttachmentText(t *testing.T) {
	e := newEnv(t)
	rec := e.room("attachment-text")
	const want = "line one\nline two\n"
	attachment, err := e.svc.Attach(rec.ID, "note.txt", []byte(want))
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.AttachmentText(rec.ID, attachment.ID)
	if err != nil || got != want {
		t.Fatalf("text %q, %v", got, err)
	}
	if _, err := e.svc.AttachmentText(rec.ID, "missing"); err == nil {
		t.Fatal("missing attachment accepted")
	}
}

func TestTranscriptPage(t *testing.T) {
	e := newEnv(t)
	rec := e.room("transcript-pages")
	lead := e.start(rec, "lead", nil)
	base := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	for n := 1; n <= 8; n++ {
		data, err := json.Marshal(subagent.Turn{N: n, Status: subagent.TurnOK, Started: base.Add(time.Duration(n) * time.Minute), Ended: base.Add(time.Duration(n)*time.Minute + 30*time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		seedTurnRow(t, e.mgr.Store, lead.ID, n, data)
		e.svc.Notify.Rooms.Now = func() time.Time { return base.Add(time.Duration(n)*time.Minute + 10*time.Second) }
		if _, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: lead.Address(), Conversation: room.DMConversation(lead.ID), To: []string{rec.Name}, Text: strconv.Itoa(n)}); err != nil {
			t.Fatal(err)
		}
	}
	e.svc.Notify.Rooms.Now = nil
	if _, err := e.mgr.Store.UpdateMeta(lead.ID, func(meta *subagent.Meta) { meta.Turns = 8 }); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{-1, 0, 1000} {
		page, err := e.svc.TranscriptPage(lead.ID, "", limit)
		if err != nil || len(page.Turns) != 8 {
			t.Fatalf("default/capped limit %d: %d turns, %v", limit, len(page.Turns), err)
		}
	}
	cursor := ""
	for _, want := range [][]int{{6, 7, 8}, {3, 4, 5}, {1, 2}} {
		page, err := e.svc.TranscriptPage(lead.ID, cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Turns) != len(want) || len(page.Posts) != len(want) {
			t.Fatalf("page: turns=%v posts=%v", page.Turns, page.Posts)
		}
		for i, n := range want {
			if page.Turns[i].N != n || page.Posts[i].Text != strconv.Itoa(n) {
				t.Fatalf("row %d: turn=%d post=%s, want %d", i, page.Turns[i].N, page.Posts[i].Text, n)
			}
		}
		if page.Earlier != (want[0] > 1) || page.Cursor != strconv.Itoa(want[0]) {
			t.Fatalf("metadata: earlier=%v cursor=%q", page.Earlier, page.Cursor)
		}
		cursor = page.Cursor
	}
	empty, err := e.svc.TranscriptPage(lead.ID, "1", 3)
	if err != nil || len(empty.Turns) != 0 || len(empty.Posts) != 0 || empty.Earlier {
		t.Fatalf("before first: %+v %v", empty, err)
	}
	for _, cursor := range []string{"bad", "-1", "0", "9"} {
		if _, err := e.svc.TranscriptPage(lead.ID, cursor, 3); err == nil {
			t.Fatalf("accepted cursor %q", cursor)
		}
	}
	for n := 9; n <= 208; n++ {
		data, err := json.Marshal(subagent.Turn{N: n, Status: subagent.TurnOK})
		if err != nil {
			t.Fatal(err)
		}
		seedTurnRow(t, e.mgr.Store, lead.ID, n, data)
	}
	if _, err := e.mgr.Store.UpdateMeta(lead.ID, func(meta *subagent.Meta) { meta.Turns = 208 }); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ limit, want int }{{0, 50}, {-1, 50}, {1000, 200}} {
		page, err := e.svc.TranscriptPage(lead.ID, "", tc.limit)
		if err != nil || len(page.Turns) != tc.want || page.Turns[0].N != 209-tc.want {
			t.Fatalf("bounded limit %d: %d turns, %v", tc.limit, len(page.Turns), err)
		}
	}

}

func TestTranscriptPageWithoutTurnsKeepsConversation(t *testing.T) {
	e := newEnv(t)
	rec := e.room("empty-transcript")
	lead := e.start(rec, "lead", nil)
	if _, err := e.mgr.Store.UpdateMeta(lead.ID, func(meta *subagent.Meta) { meta.Turns = 0 }); err != nil {
		t.Fatal(err)
	}
	att, err := e.svc.Notify.Rooms.PutAttachment(rec.ID, "note.txt", []byte("peer attachment"))
	if err != nil {
		t.Fatal(err)
	}
	post, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: rec.Name + ":peer", To: []string{lead.Address()},
		SessionIDs: map[string]string{lead.Address(): lead.ID}, Text: "peer message", Attachments: []room.Attachment{att}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.TranscriptPage(lead.ID, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	want, err := e.svc.Transcript(lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A peer's post to the session is a reference row, and its file is still
	// the session's media, so a page without turns keeps both.
	if len(got.Turns) != 0 || len(got.Posts) != 0 || len(got.References) != 1 || got.References[0].PostID != post.ID || len(got.Media) != 1 {
		t.Fatalf("zero-turn page lost conversation: %+v", got)
	}
	if got.Dir != want.Dir || got.Member.Emoji != want.Member.Emoji || got.Usage != want.Usage {
		t.Fatalf("zero-turn page lost session metadata")
	}
}

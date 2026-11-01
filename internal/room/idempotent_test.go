package room

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestStablePostIDIsIdempotentAndCannotChangeContent(t *testing.T) {
	store := &Store{Root: t.TempDir()}
	r, err := store.Create(CreateInput{Name: "posts"})
	if err != nil {
		t.Fatal(err)
	}
	in := PostInput{ID: "msg_0123456789abcdef", From: r.Name, To: []string{r.Name}, Text: "durable batch", NeedsRoot: true, Notice: EventNotice{Kind: "ready"}}
	first, err := store.Post(r.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Post(r.ID, in)
	if err != nil || first.ID != second.ID || first.TS != second.TS {
		t.Fatalf("stable retry: %+v %+v %v", first, second, err)
	}
	posts, err := store.Messages(r.ID)
	if err != nil || len(posts) != 1 || posts[0].Notice != in.Notice || !posts[0].NeedsRoot {
		t.Fatalf("duplicate posts: %+v %v", posts, err)
	}
	in.NeedsRoot = false
	if _, err = store.Post(r.ID, in); err == nil {
		t.Fatal("post ID changed its root wake intent")
	}
	in.NeedsRoot = true
	in.Notice.Kind = "changes"
	if _, err = store.Post(r.ID, in); err == nil {
		t.Fatal("post ID was reused for different structured content")
	}
	in.Notice = first.Notice
	in.Text = "different content"
	if _, err = store.Post(r.ID, in); err == nil {
		t.Fatal("post ID was reused for different content")
	}
	in.ID = "../../invalid"
	if _, err = store.Post(r.ID, in); err == nil {
		t.Fatal("invalid ID accepted")
	}
}

// appendLine adds one raw line to a room's log, as an old or damaged log holds.
func appendLine(t *testing.T, s *Store, roomID string, line []byte) {
	t.Helper()
	path, err := s.messagesPath(roomID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}

// A log that holds an ID twice answers a retry with the first post in append
// order, and the retry is compared with that post.
func TestKeyedRetryMatchesTheFirstPostOfADuplicateID(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "posts"})
	if err != nil {
		t.Fatal(err)
	}
	in := PostInput{ID: "msg_0123456789abcdef", From: rec.Name, To: []string{rec.Name}, Text: "first"}
	first, err := s.Post(rec.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	later := first
	later.Text, later.TS = "second", first.TS+1
	line, err := json.Marshal(later)
	if err != nil {
		t.Fatal(err)
	}
	appendLine(t, s, rec.ID, line)
	retry, err := s.Post(rec.ID, in)
	if err != nil || retry.Text != "first" || retry.TS != first.TS {
		t.Fatalf("retry = %+v, %v; want the first post", retry, err)
	}
	in.Text = "second"
	if _, err := s.Post(rec.ID, in); !errors.Is(err, ErrPostIDConflict) {
		t.Fatalf("retry of the later duplicate = %v, want a conflict with the first post", err)
	}
	posts, err := s.Messages(rec.ID)
	if err != nil || len(posts) != 2 {
		t.Fatalf("posts = %+v, %v; want the two stored lines only", posts, err)
	}
}

// A keyed post finds its retry through the log index, not through a copy of
// the log. A copy stops at a line longer than one read, so it missed every post
// after that line and stored the retry twice.
func TestKeyedRetryFindsAPostAfterAnOversizedLine(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "posts"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(rec.ID, PostInput{From: rec.Name, To: []string{rec.Name}, Text: "before"}); err != nil {
		t.Fatal(err)
	}
	appendLine(t, s, rec.ID, []byte(strings.Repeat("x", 5<<20)))
	in := PostInput{ID: "msg_0123456789abcdef", From: rec.Name, To: []string{rec.Name}, Text: "after"}
	first, err := s.Post(rec.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.Post(rec.ID, in)
	if err != nil || retry.TS != first.TS {
		t.Fatalf("retry = %+v, %v; want the stored post", retry, err)
	}
	path, err := s.messagesPath(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(raw), `"after"`); got != 1 {
		t.Fatalf("the log holds the post %d times, want 1", got)
	}
}

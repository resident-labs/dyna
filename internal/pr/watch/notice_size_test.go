package watch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/room"
)

func TestNoticeFitsRoomLimitAndKeepsLogEnds(t *testing.T) {
	r := Record{Key: "repo#1", URL: "https://github.com/owner/repo/pull/1", Head: strings.Repeat("a", 40)}
	text := "CI failed\n\nLog tail:\nLOG HEAD\n" + strings.Repeat("middle 界\n", 5000) + "LOG TAIL"
	notice(&r, "author", text, "failure")
	got := r.Notices[0].Text
	if len(got) >= room.MaxMessageBytes {
		t.Fatalf("notice has %d bytes; room limit is %d", len(got), room.MaxMessageBytes)
	}
	for _, want := range []string{r.URL, "Head: " + r.Head, "CI failed", "LOG HEAD", "LOG TAIL", "bytes cut"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in bounded notice", want)
		}
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split UTF-8")
	}
}

func TestQueuedOversizedNoticeCanBeDelivered(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	raw := "Header\n\nLog tail:\nLOG HEAD\n" + strings.Repeat("x", 40000) + "LOG TAIL"
	if err := s.Store.update(func(st *State) error {
		st.Records["old"] = &Record{Key: "old", State: "closed", Notices: []Notice{{ID: "legacy", Text: raw}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.Deliver = func(_ context.Context, _ Record, n Notice) (string, error) {
		if len(n.Text) > room.MaxMessageBytes {
			return "", errors.New("post is too long")
		}
		if !strings.Contains(n.Text, "LOG HEAD") || !strings.Contains(n.Text, "LOG TAIL") {
			t.Fatal("lost log ends")
		}
		return "stored", nil
	}
	if err := s.flush(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Store.Get("old")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Notices[0].Done || len(got.Notices[0].Text) >= room.MaxMessageBytes {
		t.Fatalf("oversized notice was not repaired: %+v", got.Notices[0])
	}
}

func TestFailedNoticeDoesNotStarveLaterNoticesOrPRs(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	if err := s.Store.update(func(st *State) error {
		st.Records["old"] = &Record{Key: "old", State: "closed", Notices: []Notice{{ID: "bad", Text: "bad"}, {ID: "next", Text: "next"}}}
		st.Records["other"] = &Record{Key: "other", State: "closed", Notices: []Notice{{ID: "other", Text: "other"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.Deliver = func(_ context.Context, _ Record, n Notice) (string, error) {
		if n.ID == "bad" {
			return "", errors.New("temporary delivery failure")
		}
		return "post-" + n.ID, nil
	}
	if err := s.dispatchAll(context.Background()); err == nil {
		t.Fatal("delivery error was not reported")
	}
	old, _ := s.Store.Get("old")
	other, _ := s.Store.Get("other")
	if old.Notices[0].Done || old.Notices[0].Error == "" {
		t.Fatal("failed notice lost retry state")
	}
	if !old.Notices[1].Done || !other.Notices[0].Done {
		t.Fatal("failed notice starved later delivery")
	}
}

func TestNoticeDeadlineDoesNotStopNextPRPoll(t *testing.T) {
	e := newEnv(t)
	e.track()
	e.s.noticeTimeout = 20 * time.Millisecond
	if err := e.s.Store.update(func(st *State) error {
		st.Records["000-stalled"] = &Record{Key: "000-stalled", State: "closed", Notices: []Notice{{ID: "stalled", Text: "stalled"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.s.Deliver = func(ctx context.Context, _ Record, n Notice) (string, error) {
		if n.ID == "stalled" {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("delivery has no deadline")
			}
			<-ctx.Done()
			return "", ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("later delivery inherited expired context: %v", err)
		}
		return "post", nil
	}
	if _, err := e.s.Tick(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline was not reported: %v", err)
	}
	if e.record().Checked.IsZero() {
		t.Fatal("stalled delivery prevented the next PR poll")
	}
	stalled, _ := e.s.Store.Get("000-stalled")
	if stalled.Notices[0].Done || !strings.Contains(stalled.Notices[0].Error, "deadline exceeded") {
		t.Fatal("stalled notice lost retry state")
	}
}

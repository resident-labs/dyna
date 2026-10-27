package room

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
)

func clearStamp(t *testing.T) {
	t.Helper()
	storetest.ClearStamp(t)
}

// workRoom creates the room "work" in the default store.
func workRoom(t *testing.T) room.Record {
	t.Helper()
	rec, err := room.DefaultStore().Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func execCmd(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), err
}

// A malformed `@run:<id>/agent:<n>` mention is refused before anything is stored.
func TestPostCmdRefusesMalformedRunAgentMentions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	workRoom(t)
	if _, err := execCmd(t, PostCmd(), "@run:wf_x/agent:not-a-number", "message"); err == nil || !strings.Contains(err.Error(), "invalid workflow agent mention") {
		t.Fatalf("invalid agent id error = %v", err)
	}
	if _, err := execCmd(t, PostCmd(), "@run:wf_x", "message"); err == nil || !strings.Contains(err.Error(), "@run:<run-id>/agent:<n>") {
		t.Fatalf("missing agent error = %v", err)
	}
}

// A stored post with a failed delivery comes back as a result with no
// transport error. The command prints the post and still exits non-zero with
// its id, so the text is not sent a second time.
func TestPostCmdReportsAFailedDeliveryOfAStoredPost(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	work := workRoom(t)
	saved := sendPost
	t.Cleanup(func() { sendPost = saved })
	// The engine returns the stored post with no error when only a delivery
	// failed, so the command must turn that result into its failure.
	sendPost = func(context.Context, notify.PostRequest) (notify.Result, error) {
		return notify.Result{
			Message:    room.Message{ID: "msg_0123456789abcdef", RoomID: work.ID, From: "work", Text: "read the log"},
			Deliveries: []notify.Delivery{{To: "run:wf_ended/agent:1", Delivery: "failed", Error: "run wf_ended is not running (status ok)"}},
		}, nil
	}
	out, err := execCmd(t, PostCmd(), "@lead", "read", "the", "log")
	if err == nil || !strings.Contains(err.Error(), "post msg_0123456789abcdef") {
		t.Fatalf("failed delivery error = %v", err)
	}
	if !strings.Contains(out, "post msg_0123456789abcdef in work") || !strings.Contains(out, ": failed") {
		t.Fatalf("stored post output = %q", out)
	}
}

// The bare-address grammar is gone: a post needs a mention.
func TestPostCmdRequiresMentions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	if _, err := execCmd(t, PostCmd(), "work", "hello"); err == nil || !strings.Contains(err.Error(), "@mention") {
		t.Fatalf("bare address err = %v", err)
	}
	if _, err := execCmd(t, PostCmd(), "@lead", "hello"); err == nil || !strings.Contains(err.Error(), "no room context") {
		t.Fatalf("unqualified external mention err = %v", err)
	}
}

// dyna post --forward takes the normal post path: the stored post carries
// the original, the text is the note, and a forward needs no text.
func TestPostCmdForwardsAPost(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	store := room.DefaultStore()
	rec, err := store.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	orig, err := store.Post(rec.ID, room.PostInput{From: "alpha:worker", To: []string{"alpha"}, Text: "found the bug"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := execCmd(t, PostCmd(), "--forward", orig.ID, "@alpha", "look at this")
	if err != nil || !strings.Contains(out, "in alpha") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if _, err := execCmd(t, PostCmd(), "--forward", orig.ID, "@alpha"); err != nil {
		t.Fatalf("a forward without a note: %v", err)
	}
	if _, err := execCmd(t, PostCmd(), "--forward", "msg_missing", "@alpha", "note"); err == nil || !strings.Contains(err.Error(), "cannot forward") {
		t.Fatalf("missing post err = %v", err)
	}
	msgs, _ := store.Messages(rec.ID)
	if len(msgs) != 3 {
		t.Fatalf("stored %d posts", len(msgs))
	}
	if f := msgs[1].Forward; f == nil || f.ID != orig.ID || f.Text != "found the bug" || msgs[1].Text != "look at this" {
		t.Fatalf("post = %#v", msgs[1])
	}
	if msgs[2].Forward == nil || msgs[2].Text != "" {
		t.Fatalf("noteless post = %#v", msgs[2])
	}
}

// A watcher that has not attached races the internal root for posts to @root,
// so watch warns until the external orchestrator holds the seat.
func TestWatchWarnsUntilAttached(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	work := workRoom(t)
	watch := func() string {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cmd := RoomCmd()
		var stderr bytes.Buffer
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&stderr)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs([]string{"watch", "work"})
		_ = cmd.ExecuteContext(ctx)
		return stderr.String()
	}
	if got := watch(); !strings.Contains(got, "dyna root attach --room work") {
		t.Fatalf("unattached watch stderr = %q", got)
	}
	if _, _, err := room.DefaultStore().SetSeat(context.Background(), work.ID, room.Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := watch(); got != "" {
		t.Fatalf("attached watch stderr = %q", got)
	}
}

// A room whose sessions are paused lists as paused, not active.
func TestListShowsPausedRooms(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	work := workRoom(t)
	if _, err := room.DefaultStore().SetSessionsPaused(context.Background(), work.ID, true); err != nil {
		t.Fatal(err)
	}
	out, err := execCmd(t, RoomCmd(), "list")
	if err != nil || !strings.Contains(out, "work\t"+work.ID+"\tpaused\t") {
		t.Fatalf("room list = %q, %v; want work paused", out, err)
	}
}

// An owner script that runs dyna post @<room>:root wakes root. With --quiet
// the same post is stored as next-turn context and wakes no one.
func TestPostCmdOwnerScriptWakesRootUnlessQuiet(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	clearStamp(t)
	work := workRoom(t)
	for _, tc := range []struct {
		args []string
		want room.RootDecision
	}{
		{[]string{"@work:root", "player", "asked"}, room.RootWake},
		{[]string{"--quiet", "@work:root", "player", "chatted"}, room.RootQuiet},
	} {
		if _, err := execCmd(t, PostCmd(), tc.args...); err != nil {
			t.Fatal(err)
		}
		msgs, err := room.DefaultStore().Messages(work.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := room.RootContext(msgs[len(msgs)-1], "work"); got != tc.want {
			t.Fatalf("%v: root decision = %d, want %d", tc.args, got, tc.want)
		}
	}
}

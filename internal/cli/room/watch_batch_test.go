package room

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"dyna-agent/internal/room"
)

func TestWatchHoldsLoneQuietPostUntilWake(t *testing.T) {
	var out bytes.Buffer
	write := watchWriter(&out, "work", false, false)
	quiet := room.Message{ID: "quiet", From: "dyna", Kind: "notice", To: []string{"work"}, Text: "Cleanup complete"}
	if err := write(quiet); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("lone quiet post printed before a wake: %s", out.Bytes())
	}
	wake := room.Message{Wake: true, ID: "wake", From: "work:lead", Kind: "alert", To: []string{"work"}, Text: "Main is red"}
	if err := write(wake); err != nil {
		t.Fatal(err)
	}
	var got watchDelivery
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != wake.ID || !reflect.DeepEqual(got.Posts, []room.Message{quiet, wake}) || bytes.Count(out.Bytes(), []byte{'\n'}) != 1 {
		t.Fatalf("quiet-plus-wake delivery = %s", out.Bytes())
	}
	out.Reset()
	if err := write(wake); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got.Posts, []room.Message{wake}) {
		t.Fatalf("held context repeated: %s, %v", out.Bytes(), err)
	}
}

func TestWatchAllKeepsRawQuietPosts(t *testing.T) {
	for _, msg := range []room.Message{
		{ID: "quiet", From: "dyna", Kind: "notice", To: []string{"work"}},
		{ID: "self", From: "work:root", Kind: "completed", To: []string{"work"}},
	} {
		var out bytes.Buffer
		if err := watchWriter(&out, "work", true, false)(msg); err != nil {
			t.Fatal(err)
		}
		var got room.Message
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, msg) {
			t.Fatalf("raw --all post = %s, %v", out.Bytes(), err)
		}
	}
}

func TestWatchWakeAndOwnerEchoRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  room.Message
		want bool
	}{
		{"owner request", room.Message{Wake: true, From: "work", Human: true, Kind: "message", To: []string{"work"}}, true},
		// The owner's DM to an agent is quiet context for both roots: the
		// agent answers it, and root reads what was said with the next wake.
		{"owner DM to an agent", room.Message{Wake: true, From: "work", Human: true, Kind: "message", Via: []string{"@worker"}, To: []string{"work:worker"}}, false},
		{"owner failure", room.Message{Wake: true, From: "work:worker", Kind: "failed", OwnerReport: true, To: []string{"work"}}, true},
		{"own owner action", room.Message{Wake: true, From: "work", Kind: "message", NeedsRoot: true, To: []string{"work"}}, false},
		{"own owner alert", room.Message{Wake: true, From: "work", Kind: "alert", To: []string{"work"}}, false},
		{"another recipient", room.Message{Wake: true, From: "work:worker", Kind: "alert", To: []string{"work:other"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			write := watchWriter(&out, "work", false, false)
			quiet := room.Message{ID: "quiet", From: "dyna", Kind: "notice", To: []string{"work"}}
			if err := write(quiet); err != nil {
				t.Fatal(err)
			}
			tc.msg.ID = "next"
			if err := write(tc.msg); err != nil {
				t.Fatal(err)
			}
			if !tc.want {
				if out.Len() != 0 {
					t.Fatalf("unrelated or own post flushed context: %s", out.Bytes())
				}
				return
			}
			var got watchDelivery
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got.Posts, []room.Message{quiet, tc.msg}) {
				t.Fatalf("root wake delivery = %s, %v", out.Bytes(), err)
			}
		})
	}
}

func TestWatchHoldsQuietContextThroughRootSelfPosts(t *testing.T) {
	var out bytes.Buffer
	write := watchWriter(&out, "work", false, false)
	quiet := room.Message{ID: "quiet", From: "work:lead", Kind: "notice", To: []string{"work"}, Text: "Keep the queue paused"}
	if err := write(quiet); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"message", "completed", "failed"} {
		self := room.Message{ID: "self-" + kind, From: "work:root", Kind: kind, To: []string{"work"}, Text: "Root history"}
		if err := write(self); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 {
			t.Errorf("root self-post flushed held context: %s", out.Bytes())
		}
	}
	alert := room.Message{Wake: true, ID: "alert", From: "work:lead", Kind: "alert", To: []string{"work"}, Text: "Main is red"}
	if err := write(alert); err != nil {
		t.Fatal(err)
	}
	var got watchDelivery
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.ID != alert.ID || !reflect.DeepEqual(got.Posts, []room.Message{quiet, alert}) {
		t.Fatalf("held context did not wait for the real alert: %s, %v", out.Bytes(), err)
	}
}

// The engine's timed-flush notice, posted after 15 quiet minutes, delivers
// the held context as one block. Its shape must stay in sync with the notice
// rootFlush.fire posts (internal/engine/rootflush.go).
func TestWatchTimedFlushNoticeDeliversHeldContext(t *testing.T) {
	var out bytes.Buffer
	write := watchWriter(&out, "work", false, false)
	quiet := room.Message{ID: "quiet", From: "work:lead", Kind: "notice", To: []string{"work"}, Text: "Keep the queue paused"}
	if err := write(quiet); err != nil {
		t.Fatal(err)
	}
	flush := room.Message{ID: "flush", Wake: true, NeedsRoot: true, From: "dyna", Kind: "notice", To: []string{"work"}, Text: "[TIMED FLUSH] No post woke the root seat for 15 minutes."}
	if err := write(flush); err != nil {
		t.Fatal(err)
	}
	var got watchDelivery
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.ID != flush.ID || !reflect.DeepEqual(got.Posts, []room.Message{quiet, flush}) {
		t.Fatalf("timed-flush delivery = %s, %v", out.Bytes(), err)
	}
}

func TestWatchHoldsPassiveInputUntilWake(t *testing.T) {
	var out bytes.Buffer
	write := watchWriter(&out, "work", false, false)
	quiet := room.Message{ID: "quiet", From: "work:worker", Kind: "issue", To: []string{"work"}, Text: "Repair the test"}
	passive := room.Message{ID: "passive", From: "dyna", Kind: "instructions", To: []string{"work:root"}, Text: "Updated room rules"}
	for _, msg := range []room.Message{quiet, passive} {
		if err := write(msg); err != nil {
			t.Fatal(err)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("passive input released held context: %s", out.Bytes())
	}
	wake := room.Message{ID: "wake", Wake: true, From: "work:worker", Kind: "alert", To: []string{"work"}, Text: "Main is red"}
	if err := write(wake); err != nil {
		t.Fatal(err)
	}
	var got watchDelivery
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.ID != wake.ID || !reflect.DeepEqual(got.Posts, []room.Message{quiet, passive, wake}) {
		t.Fatalf("passive input must arrive once with the next wake: %s, %v", out.Bytes(), err)
	}
}

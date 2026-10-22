package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
)

type watchBlock struct {
	ID    string         `json:"id"`
	Posts []room.Message `json:"posts"`
}

type watchBlocks chan watchBlock

func (out watchBlocks) Write(data []byte) (int, error) {
	var block watchBlock
	if err := json.Unmarshal(data, &block); err != nil {
		return 0, err
	}
	out <- block
	return len(data), nil
}

func TestRootWatchReceivesStewardPosts(t *testing.T) {
	for _, external := range []bool{false, true} {
		name := "internal"
		if external {
			name = "external"
		}
		t.Run(name, func(t *testing.T) {
			storetest.Isolate(t)
			st := room.DefaultStore()
			rec := workRoom(t)
			if _, _, err := st.SetSeat(t.Context(), rec.ID, room.Seat{External: external}, nil); err != nil {
				t.Fatal(err)
			}
			steward, root := rec.Name+":merge-steward", rec.Name+":"+room.RootAgent
			post := func(in room.PostInput) room.Message {
				t.Helper()
				msg, err := st.Post(rec.ID, in)
				if err != nil {
					t.Fatal(err)
				}
				return msg
			}
			post(room.PostInput{Wake: true, From: steward, Kind: "alert", To: []string{rec.Name}, Text: "Before cursor"})
			cursor := post(room.PostInput{From: "dyna", Kind: "instructions", To: []string{rec.Name}, Text: "Hidden cursor"})
			var quiet []room.Message
			for _, in := range []room.PostInput{
				{From: steward, Kind: "notice", To: []string{root}, Text: "Quiet steward note"},
				{From: steward, Kind: "merge-decision", To: []string{rec.Name}, Text: "Keep the failed trial"},
				{From: steward, Kind: "issue", To: []string{rec.Name}, Text: "Repair the test"},
			} {
				msg := post(in)
				quiet = append(quiet, msg)
				if msg.Wake || msg.NeedsRoot || room.WakesRoot(msg, rec.Name) {
					t.Fatalf("quiet post acquired root wake intent: %+v", msg)
				}
			}
			for _, in := range []room.PostInput{
				{From: steward, Kind: "alert", To: []string{rec.Name + ":worker"}, Text: "Another recipient"},
				{From: steward, Kind: "merge-decision", To: []string{"another-room"}, Text: "Another room"},
				{From: rec.Name, To: []string{rec.Name}, Text: "Own owner post"},
			} {
				post(in)
			}
			alert := post(room.PostInput{Wake: true, From: steward, Kind: "alert", To: []string{root}, Text: "Main is red"})
			watch := func(since string) (watchBlocks, context.CancelFunc) {
				t.Helper()
				ctx, cancel := context.WithCancel(t.Context())
				out, done := make(watchBlocks, 32), make(chan error, 1)
				cmd := watchCmd()
				cmd.SetArgs([]string{rec.Name, "--since", since})
				cmd.SetOut(out)
				cmd.SetErr(io.Discard)
				go func() { done <- cmd.ExecuteContext(ctx) }()
				t.Cleanup(func() {
					cancel()
					select {
					case err := <-done:
						if !errors.Is(err, context.Canceled) {
							t.Errorf("room watch stopped: %v", err)
						}
					case <-time.After(30 * time.Second):
						t.Error("room watch did not stop")
					}
				})
				return out, cancel
			}
			readThrough := func(out watchBlocks, last string) []watchBlock {
				t.Helper()
				guard := time.NewTimer(30 * time.Second)
				defer guard.Stop()
				var blocks []watchBlock
				for {
					select {
					case block := <-out:
						blocks = append(blocks, block)
						if block.ID == last {
							return blocks
						}
					case <-guard.C:
						t.Fatal("room watch missed the waking post")
					}
				}
			}
			out, cancel := watch(cursor.ID)
			want := []watchBlock{{ID: alert.ID, Posts: append(quiet, alert)}}
			if got := readThrough(out, alert.ID); !reflect.DeepEqual(got, want) {
				t.Errorf("root watch deliveries = %+v; want one ordered quiet-plus-alert block %+v", got, want)
			}
			// The first waking post proves the real watcher is running. A
			// later quiet cleanup notice waits for the next action request.
			cleanup := post(room.PostInput{From: "dyna", Kind: "notice", To: []string{rec.Name}, Text: "Quiet cleanup note"})
			wake := post(room.PostInput{From: steward, Kind: "message", To: []string{rec.Name}, Wake: true, NeedsRoot: true, Text: "Choose the recovery"})
			want = []watchBlock{{ID: wake.ID, Posts: []room.Message{cleanup, wake}}}
			if got := readThrough(out, wake.ID); !reflect.DeepEqual(got, want) {
				t.Errorf("live deliveries = %+v; want %+v", got, want)
			}
			cancel()
			// A watcher that stops with held notes keeps the last waking ID
			// as its cursor. Restart replays the notes with the next wake.
			restarted, _ := watch(alert.ID)
			if got := readThrough(restarted, wake.ID); !reflect.DeepEqual(got, want) {
				t.Errorf("resumed deliveries = %+v; want %+v", got, want)
			}
		})
	}
}

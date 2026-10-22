package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

func TestClaimReplyWakeFollowsQueueChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change bool
	}{
		{name: "empty_reply"},
		{name: "queue_claim", change: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			info := completedFixtureSession(t, e)
			var messageID string
			if tc.change {
				messageID = postClaimInput(t, e, info, "claim once").ID
			}
			dir, err := e.m.Store.Dir(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			before, beforeVersion, err := readQueueRow(dir)
			if err != nil {
				t.Fatal(err)
			}
			dataDir := filepath.Dir(e.m.Store.root())
			if err := state.MigrateRequests(dataDir); err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			rid := info.ID + "/claim/wake"
			key := "runner/" + rid
			events := &eventLog{}
			publish := func(ev hub.Event) {
				events.add(ev)
				if !tc.change || ev.Topic != sessionlife.Topic || ev.Key != info.ID {
					return
				}
				// A separate reader must see both the queue and exact reply
				// when the committed queue change reaches its listeners.
				raw, _, queueErr := queryQueueRow(db, info.ID)
				var queue queueClaims
				decodeErr := json.Unmarshal(raw, &queue)
				saved, done, receiptErr := state.For(db).RequestDone(t.Context(), key)
				var reply ClaimedInput
				replyErr := json.Unmarshal(saved, &reply)
				if queueErr != nil || decodeErr != nil || receiptErr != nil || replyErr != nil || !done || queue.Claimed[messageID] != 1 || len(reply.Posts) != 1 || reply.Posts[0].Steer.ID != messageID {
					t.Errorf("event preceded queue/reply commit: queue %s, reply %s, done %v; errors %v %v %v %v", raw, saved, done, queueErr, decodeErr, receiptErr, replyErr)
				}
			}
			fix := &claimFixture{e: e, info: info, dir: dir, dataDir: dataDir, events: events}
			engine := fix.serve(t)
			t.Cleanup(func() { _ = engine.srv.Close() })
			engine.publish = publish
			state.For(db).SetPublish(publish)
			call := func() json.RawMessage {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				var reply json.RawMessage
				err := engine.client.Call(callspec.WithRequest(ctx, rid), "RunnerClaimInput", []any{InputClaim{Turn: 1, Startup: true}}, &reply)
				if err != nil {
					t.Fatal(err)
				}
				return reply
			}

			beforeWrites := state.For(db).Health().Writes
			first := call()
			var claimed ClaimedInput
			if err := json.Unmarshal(first, &claimed); err != nil {
				t.Fatal(err)
			}
			wantEvents := 0
			if tc.change {
				wantEvents = 1
				if len(claimed.Posts) != 1 || claimed.Posts[0].Steer.ID != messageID {
					t.Fatalf("queue claim returned %s", first)
				}
			} else if len(claimed.Posts) != 0 {
				t.Fatalf("empty claim returned posts: %s", first)
			}
			if got := events.count(sessionlife.Topic, info.ID); got != wantEvents {
				t.Errorf("first claim published %d session events; want %d", got, wantEvents)
			}
			if !tc.change {
				if writes := state.For(db).Health().Writes - beforeWrites; writes != 0 {
					t.Fatalf("empty claim made %d SQL writes; want none", writes)
				}
				after, version, err := readQueueRow(dir)
				if err != nil || !bytes.Equal(after, before) || version != beforeVersion {
					t.Fatalf("reply-only claim changed the queue: %s, version %q; before %s, %q; error %v", after, version, before, beforeVersion, err)
				}
			}
			if err := engine.reopen(); err != nil {
				t.Fatal(err)
			}
			reopened, err := database.Open(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			saved, done, err := state.For(reopened).RequestDone(t.Context(), key)
			if err != nil || done != tc.change || tc.change && !bytes.Equal(saved, first) || !tc.change && len(saved) != 0 {
				t.Fatalf("reopened receipt = %s, done %v, error %v; want %s", saved, done, err, first)
			}
			replay := call()
			calls, claims, replays, cached, wasReopened, firstWriter, retryWriter := engine.snapshot()
			valid := bytes.Equal(replay, first) && slices.Equal(calls, []string{rid, rid}) && wasReopened
			if tc.change {
				valid = valid && bytes.Equal(cached, first) && claims == 1 && replays == 1 && firstWriter != retryWriter
			} else {
				valid = valid && len(cached) == 0 && claims == 2 && replays == 0
				if writes := state.For(reopened).Health().Writes; writes != 0 {
					t.Fatalf("recomputed empty claim made %d SQL writes", writes)
				}
			}
			if !valid {
				t.Fatalf("replay changed the reply or repeated the claim: first %s, replay %s, cached %s; calls %v, claims %d, replays %d, reopened %v", first, replay, cached, calls, claims, replays, wasReopened)
			}
			if got := events.count(sessionlife.Topic, info.ID); got != wantEvents {
				t.Errorf("claim and replay published %d session events; want %d", got, wantEvents)
			}
			if err := engine.faultNow(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

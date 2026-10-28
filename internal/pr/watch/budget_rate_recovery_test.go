package watch

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	prstate "dyna-agent/internal/pr/state"
	appstate "dyna-agent/internal/state"
)

func TestMovedBudgetTargetKeepsScopedRateEvidence(t *testing.T) {
	for _, move := range []string{"head", "room", "removed"} {
		t.Run(move, func(t *testing.T) {
			e, snap := completedBudgetFixture(t)
			runBudgetFixture(t, e, snap)
			r := e.record()
			alice := (RateLimitScope{Host: r.Host, Account: "alice", Resource: "core"}).Key()
			bob := (RateLimitScope{Host: r.Host, Account: "bob", Resource: "core"}).Key()
			prior, observed, other := 1500, 900, 2500
			reset := e.clock.Add(time.Hour)
			if err := e.s.Store.update(func(st *State) error {
				st.RateLimits[alice] = RateLimit{Remaining: &prior, Reset: reset, ObservedAt: e.clock}
				st.RateLimits[bob] = RateLimit{Remaining: &other, Reset: reset, ObservedAt: e.clock}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			st, err := e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			gh := e.s.github(r.RoomID)
			rates := cloneRates(st.RateLimits)
			rates[alice] = RateLimit{Remaining: &observed, Reset: reset, ObservedAt: e.clock.Add(time.Second)}
			stalePath := "github.com/owner/repo/pulls/17/reviews"
			gh.requests = &requestCache{entries: st.Responses[r.Key], used: map[string]cachedResponse{stalePath: {Body: []byte("[]")}}, rates: rates, now: e.s.now}
			db, err := database.Open(filepath.Dir(e.s.Store.Root))
			if err != nil {
				t.Fatal(err)
			}
			writer := appstate.For(db)
			beforeWrites := writer.Health().Writes
			var published atomic.Int64
			writer.SetPublish(func(ev hub.Event) {
				if ev.Topic == prstate.Topic {
					published.Add(1)
				}
			})
			original := readBudgetSnapshot
			moved := false
			var expected Record
			readBudgetSnapshot = func(root string) (prstate.Snapshot, error) {
				snapshot, err := original(root)
				if err == nil && !moved {
					moved = true
					err = e.s.Store.update(func(st *State) error {
						switch move {
						case "head":
							st.Records[r.Key].Head = strings.Repeat("b", 40)
						case "room":
							st.Records[r.Key].RoomID = "another-room"
						case "removed":
							delete(st.Records, r.Key)
						}
						return nil
					})
					if err == nil && move != "removed" {
						expected, err = e.s.Store.Get(r.Key)
					}
				}
				return snapshot, err
			}
			t.Cleanup(func() { readBudgetSnapshot = original })
			if err := e.s.runBudgets(t.Context(), pollResult{key: r.Key, gh: gh, snap: snap}, map[string]bool{}); err != nil {
				t.Fatal(err)
			}
			after, err := e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if !moved || after.RateLimits[alice].Remaining == nil || *after.RateLimits[alice].Remaining != observed ||
				after.RateLimits[bob].Remaining == nil || *after.RateLimits[bob].Remaining != other ||
				writer.Health().Writes-beforeWrites != 2 || published.Load() != 2 {
				t.Fatalf("moved target lost or mixed rate evidence: moved=%v, rates=%v, writes=%d, publications=%d",
					moved, after.RateLimits, writer.Health().Writes-beforeWrites, published.Load())
			}
			if _, ok := after.Responses[r.Key][stalePath]; ok {
				t.Fatal("stale target response was committed with global rates")
			}
			if move == "removed" {
				if after.Records[r.Key] != nil {
					t.Fatal("rate recovery recreated the removed target")
				}
			} else if !reflect.DeepEqual(*after.Records[r.Key], expected) {
				// Get reads the stored row, whose JSON omits empty maps that the
				// write callback left in memory. Compare the persisted form: the
				// record a fresh process decodes is the outcome contract.
				got, _ := json.Marshal(after.Records[r.Key])
				want, _ := json.Marshal(&expected)
				if !bytes.Equal(got, want) {
					t.Fatalf("rate recovery changed the moved target's budget outcome\n after: %s\n want: %s", got, want)
				}
			}
		})
	}
}

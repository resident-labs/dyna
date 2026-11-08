package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type pinnedFailureBoxes struct {
	fakeBoxes
	hosts []contract.HostName
}

func (b *pinnedFailureBoxes) Create(_ context.Context, spec BoxSpec) (string, error) {
	b.hosts = append(b.hosts, spec.Host)
	return "", box.StepTimeout{Host: spec.Host, Step: contract.StartBinary}
}

// Decode the public wire request so these behavioral checks also fail before the field exists.
func TestSessionStartExplicitHost(t *testing.T) {
	for _, scenario := range []string{"placed", "capacity", "unknown", "agents off", "exec only", "host rooms", "room rule", "conflict", "failure"} {
		t.Run(scenario, func(t *testing.T) {
			s, room, parent, ctx := setup(t)
			h := box.Host{Name: "named", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: s.now()}
			switch scenario {
			case "capacity":
				h.CPUs = 0
			case "agents off":
				h.NoAgents = true
			case "exec only":
				h.ExecOnly = true
			case "host rooms":
				if err := s.store.CreateRoom(ctx, "elsewhere", "elsewhere", s.now()); err != nil {
					t.Fatal(err)
				}
				h.Rooms = []contract.RoomID{"elsewhere"}
			}
			if scenario != "unknown" {
				if err := s.store.AddHost(ctx, h); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "room rule" {
				if err := s.store.SetRoomOnly(ctx, room, []string{"host"}); err != nil {
					t.Fatal(err)
				}
			}
			var req contract.SessionStartRequest
			if err := json.Unmarshal([]byte(`{"name":"pinned","profile":"test","host":"named"}`), &req); err != nil {
				t.Fatal(err)
			}
			req.Room, req.Parent = room, parent
			req.SameHost = scenario == "conflict"
			response, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, req)
			refused := scenario != "placed" && scenario != "capacity" && scenario != "failure"
			if refused {
				if problem == nil || problem.Code != contract.ErrInvalid || !strings.Contains(strings.ToLower(problem.Message), "host") {
					t.Fatalf("expected host refusal, got %+v", problem)
				}
				var count int
				if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE name='pinned'").Scan(&count); err != nil || count != 0 {
					t.Fatalf("refused start persisted: %d %v", count, err)
				}
				return
			}
			if problem != nil {
				t.Fatal(problem)
			}
			current, err := s.store.Box(ctx, response.Box)
			if err != nil || current.Host != "named" {
				t.Fatalf("placement: %+v %v", current, err)
			}
			if scenario == "failure" {
				// Exhaust the usual per-host attempts through the real lifecycle completion path.
				boxes := &pinnedFailureBoxes{}
				s.boxes = boxes
				resetLifecycle(s)
				if err := s.store.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='creating',failures=2 WHERE id=?", response.Box)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				passLifecycle(t, s, ctx)
				passLifecycle(t, s, ctx)
				if len(boxes.hosts) != 1 || boxes.hosts[0] != "named" {
					t.Fatalf("unexpected host attempts: %v", boxes.hosts)
				}
				current, _ = s.store.Box(ctx, response.Box)
				if current.State != contract.BoxFailed || current.Host != "named" || !strings.Contains(current.Reason, "initial pinned placement") {
					t.Fatalf("pinned failure relocated: %+v", current)
				}
				return
			}
			passLifecycle(t, s, ctx)
			current, _ = s.store.Box(ctx, response.Box)
			if current.Host != "named" {
				t.Fatalf("relocated: %+v", current)
			}
			if scenario == "capacity" {
				if current.State != contract.BoxPending {
					t.Fatalf("did not wait: %+v", current)
				}
				if err := s.store.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, "UPDATE hosts SET cpus=32 WHERE name='named'")
					return err
				}); err != nil {
					t.Fatal(err)
				}
				passLifecycle(t, s, ctx)
				current, _ = s.store.Box(ctx, response.Box)
				if current.Host != "named" || current.State != contract.BoxStarting {
					t.Fatalf("did not resume on pinned host: %+v", current)
				}
			}
			if scenario == "placed" && current.State != contract.BoxStarting {
				t.Fatalf("did not create: %+v", current)
			}
		})
	}
}

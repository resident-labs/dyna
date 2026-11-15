package engine

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestSessionRestartPending(t *testing.T) {
	for _, mode := range []string{"unpinned", "keep-pin", "replace-pin", "same-host", "volume", "prior-create", "unknown-host", "room-rule", "no-agents", "exec-only"} {
		t.Run(mode, func(t *testing.T) {
			s, room, parent, ctx := setup(t)
			adapter := &restartCountBoxes{}
			s.boxes = adapter
			resetLifecycle(s)
			owner := contract.Principal{Kind: contract.PrincipalOwner}
			if err := s.store.AddHost(ctx, box.Host{Name: "other", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB}); err != nil {
				t.Fatal(err)
			}
			start := contract.SessionStartRequest{Room: room, Parent: parent, Name: "pending", Profile: "test"}
			if mode != "unpinned" {
				start.Host = "host"
			}
			if mode == "same-host" {
				start.Host = ""
				start.SameHost = true
			}
			started, problem := s.SessionStart(ctx, owner, start)
			if problem != nil {
				t.Fatal(problem)
			}
			var id contract.BoxID
			if err := s.store.Read().QueryRow(`SELECT id FROM boxes WHERE session=?`, started.Session).Scan(&id); err != nil {
				t.Fatal(err)
			}
			host := contract.HostName("")
			if mode != "unpinned" && mode != "keep-pin" {
				host = "other"
			}
			if mode == "unknown-host" {
				host = "missing"
			}
			if err := s.store.Write(ctx, func(tx *sql.Tx) error {
				var err error
				switch mode {
				case "volume":
					_, err = tx.Exec(`UPDATE boxes SET image='existing-volume' WHERE id=?`, id)
				case "prior-create":
					event, e := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: id, Session: started.Session, Host: "host", From: contract.BoxPending, To: contract.BoxCreating})
					if e != nil {
						return e
					}
					_, err = s.store.AppendEvent(tx, event)
				case "room-rule":
					_, err = tx.Exec(`UPDATE rooms SET only_hosts='["host"]' WHERE id=?`, room)
				case "no-agents":
					_, err = tx.Exec(`UPDATE hosts SET agents=0 WHERE name='other'`)
				case "exec-only":
					_, err = tx.Exec(`UPDATE hosts SET exec_only=1 WHERE name='other'`)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			_, problem = s.SessionRestart(ctx, owner, contract.SessionRestartRequest{Session: started.Session, Host: host})
			refused := mode == "volume" || mode == "prior-create" || mode == "unknown-host" || mode == "room-rule" || mode == "no-agents" || mode == "exec-only"
			if (problem != nil) != refused {
				t.Fatalf("restart: %v", problem)
			}
			if (mode == "volume" || mode == "prior-create") && !strings.Contains(problem.Message, "files") {
				t.Fatalf("refusal must explain files: %v", problem)
			}
			if adapter.calls.Load() != 0 {
				t.Fatal("pending restart reached destructive cleanup")
			}
			current, err := s.store.Box(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			expected := contract.HostName("host")
			if mode == "unpinned" {
				expected = ""
			} else if !refused && mode != "keep-pin" {
				expected = "other"
			}
			if current.Host != expected || current.State != contract.BoxPending {
				t.Fatalf("placement: %+v", current)
			}
			// Restarting an unused pending box remains safe; the lifecycle has not run yet.
			if !refused {
				if _, problem := s.SessionRestart(context.Background(), owner, contract.SessionRestartRequest{Session: started.Session}); problem != nil {
					t.Fatal(problem)
				}
			}
		})
	}
}

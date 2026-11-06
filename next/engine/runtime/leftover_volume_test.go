package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

// Real lifecycle, store, runtime Boxes, podman.Host and framed shell transport.
// Podman itself is a filesystem fixture: named workspace and stack volumes are
// separate directories, deleted only by its explicit volume rm/prune commands.
func TestStaleCreateFailureDebtPreservesNamedWork(t *testing.T) {
	for _, scenario := range []string{"ever-ready", "unknown", "first-create-unused", "explicit-discard", "reused-current-incarnation"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(100000, 0)
			root, tools := t.TempDir(), t.TempDir()
			workspace, stack, logPath := filepath.Join(root, "workspace"), filepath.Join(root, "stack"), filepath.Join(root, "commands")
			for _, dir := range []string{workspace, stack} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "user-work"), []byte("must survive Stop"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			script := `#!/bin/bash
set -eu
printf '%s\n' "$*" >> "$REVIEW_VOLUME_LOG"
case " $* " in
 *' volume rm '*) rm -rf -- "$REVIEW_WORKSPACE" ;;
 *' volume prune '*) rm -rf -- "$REVIEW_STACK" ;;
 *) : ;;
esac
`
			if err := os.WriteFile(filepath.Join(tools, "podman"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
			t.Setenv("REVIEW_VOLUME_LOG", logPath)
			t.Setenv("REVIEW_WORKSPACE", workspace)
			t.Setenv("REVIEW_STACK", stack)
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,maintained_at) VALUES('h','','ready',64,137438953472,1099511627776,?,?);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','h','','agent','creating','','',0,0)`, now.UnixMilli(), now.UnixMilli())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			sockets := api.NewSockets(nil, t.TempDir(), uint32(os.Getuid()), nil)
			defer sockets.Close()
			h, err := podman.New("user@host", t.TempDir(), "h", &nativePinnedRunner{})
			if err != nil {
				t.Fatal(err)
			}
			runtime := &Boxes{Store: st, Sockets: sockets, Layout: box.Layout{Data: root, HostData: root}, Runtime: t.TempDir(), hosts: map[contract.HostName]*podman.Host{"h": h}}
			seq := 0
			event := func(body contract.EventBody) contract.Event {
				seq++
				return contract.Event{ID: contract.EventID(fmt.Sprintf("review-volume-%d", seq)), Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body}
			}

			if scenario != "unknown" {
				if err := st.Write(ctx, func(tx *sql.Tx) error {
					_, err := st.AppendEvent(tx, event(contract.BoxChanged{Box: "b", Session: "s", To: contract.BoxPending}))
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "ever-ready" || scenario == "explicit-discard" {
				original, err := st.Box(ctx, "b")
				if err != nil {
					t.Fatal(err)
				}
				ready, err := st.TransitionBox(ctx, original, contract.BoxReady, "", event(contract.BoxChanged{}))
				if err != nil {
					t.Fatal(err)
				}
				stopped, err := st.TransitionBox(ctx, ready, contract.BoxStopped, "", event(contract.BoxChanged{}))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := st.TransitionBox(ctx, stopped, contract.BoxCreating, "", event(contract.BoxChanged{})); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE boxes SET updated_at=? WHERE id='b'", now.Add(-16*time.Minute).UnixMilli())
				return err
			}); err != nil {
				t.Fatal(err)
			}

			r := lifecycle.New(st, runtime, stopVolumeView{}, nil, func(_ contract.RoomID, _ contract.Principal, body contract.EventBody) (contract.Event, error) {
				return event(body), nil
			}, func() time.Time { return now })
			if err := r.FailStaleCreates(ctx); err != nil {
				t.Fatal(err)
			}
			leftovers, err := st.Leftovers(ctx, "h")
			if err != nil || len(leftovers) != 1 {
				t.Fatalf("failure debt: %+v %v", leftovers, err)
			}
			if scenario == "explicit-discard" {
				if err := st.Write(ctx, func(tx *sql.Tx) error { _, err := tx.Exec("UPDATE boxes SET want='discard' WHERE id='b'"); return err }); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "reused-current-incarnation" {
				if err := st.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec("UPDATE boxes SET state='creating',want='run',updated_at=? WHERE id='b'", now.UnixMilli())
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			l := leftovers[0]
			complete, err := runtime.RemoveLeftoverBox(ctx, l.Host, l.Box, l.Slice)
			if err != nil {
				t.Fatal(err)
			}
			expected := lifecycle.LeftoverContainersRemoved
			if scenario == "explicit-discard" {
				expected = lifecycle.LeftoverComplete
			}
			if scenario == "reused-current-incarnation" {
				expected = lifecycle.LeftoverDeferred
			}
			if complete != expected {
				t.Fatalf("named-volume cleanup complete=%v for %s", complete, scenario)
			}

			raw, err := os.ReadFile(logPath)
			if err != nil && !(scenario == "reused-current-incarnation" && os.IsNotExist(err)) {
				t.Fatal(err)
			}
			if scenario == "reused-current-incarnation" && len(raw) != 0 {
				t.Errorf("old debt touched current active incarnation: %s", raw)
			}
			t.Logf("scenario=%s actual failure-debt commands:\n%s", scenario, raw)
			for _, dir := range []string{workspace, stack} {
				if scenario == "explicit-discard" {
					if _, err := os.Stat(filepath.Join(dir, "user-work")); !os.IsNotExist(err) {
						t.Errorf("explicit discard failed to delete %s: %v", dir, err)
					}
					continue
				}
				if raw, err := os.ReadFile(filepath.Join(dir, "user-work")); err != nil || string(raw) != "must survive Stop" {
					t.Errorf("ordinary failure debt erased named volume %s: %q %v", dir, raw, err)
				}
			}
			if scenario != "explicit-discard" && (strings.Contains(string(raw), "volume rm") || strings.Contains(string(raw), "volume prune")) {
				t.Errorf("ordinary failure debt without destructive owner intent issued named-volume commands")
			}
		})
	}
}

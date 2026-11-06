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

type stopVolumeView struct{}

func (stopVolumeView) Connected(contract.SessionID) bool                          { return true }
func (stopVolumeView) OpenTerminal(contract.SessionID) bool                       { return false }
func (stopVolumeView) Stale(contract.SessionID) bool                              { return false }
func (stopVolumeView) SessionClosed(contract.SessionID)                           {}
func (stopVolumeView) BoxStopped(context.Context, contract.SessionID)             {}
func (stopVolumeView) CompactBeforeStop(context.Context, contract.SessionID) bool { return false }

// Real lifecycle, store, runtime Boxes, podman.Host and framed shell transport.
// Podman itself is a filesystem fixture: named workspace and stack volumes are
// separate directories, deleted only by its explicit volume rm/prune commands.
func TestStopVolumeRuleThroughActualRuntime(t *testing.T) {
	for _, scenario := range []string{"early-hello-late-result", "creating-after-ready", "stopped-no-image", "first-create-unused", "unknown-create"} {
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
			// Capture the create owner before its early hello. This is a genuine typed
			// readiness event, followed by persisted completed session work.
			original, err := st.Box(ctx, "b")
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "first-create-unused" || scenario == "unknown-create" {
				if scenario == "first-create-unused" {
					if err := st.Write(ctx, func(tx *sql.Tx) error {
						_, err := st.AppendEvent(tx, event(contract.BoxChanged{Box: "b", Session: "s", To: contract.BoxPending}))
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				if _, accepted, err := st.RequestStartStop(ctx, "s", now); err != nil || !accepted {
					t.Fatalf("Stop request: %v %v", accepted, err)
				}
			} else {
				helloEvent := event(contract.BoxChanged{})
				helloEvent.Time = now.Add(-10 * time.Second)
				ready, err := st.TransitionBox(ctx, original, contract.BoxReady, "", helloEvent)
				if err != nil {
					t.Fatal(err)
				}
				if err := st.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('used-turn','s',?,?, 'completed','p',0,0,0,0)`, now.Add(-9*time.Second).UnixMilli(), now.Add(-time.Second).UnixMilli())
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if scenario != "early-hello-late-result" {
					stopped, err := st.TransitionBox(ctx, ready, contract.BoxStopped, "", event(contract.BoxChanged{}))
					if err != nil {
						t.Fatal(err)
					}
					if scenario == "creating-after-ready" {
						if _, err := st.TransitionBox(ctx, stopped, contract.BoxCreating, "", event(contract.BoxChanged{})); err != nil {
							t.Fatal(err)
						}
					}
					if _, accepted, err := st.RequestStartStop(ctx, "s", now); err != nil || !accepted {
						t.Fatalf("Stop request: accepted=%v err=%v", accepted, err)
					}
				} else {
					// A refresh/stale stop may follow the early turn's completion
					// while Create's final result is outstanding. It keeps the volume,
					// unlike explicit session.restart/discard, and has no quiet wait.
					stopped, err := st.StopStale(ctx, []contract.SessionID{"s"})
					if err != nil || len(stopped) != 1 {
						t.Fatalf("refresh Stop: %v %v", stopped, err)
					}
				}
			}

			// The actual create-error cleanup deliberately preserves both named volumes.
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			cleanupCreate(canceled, h, lifecycle.BoxSpec{Box: "b", Host: "h"}, runtime.forward("b"), true, true, false, false)
			for _, dir := range []string{workspace, stack} {
				if _, err := os.Stat(filepath.Join(dir, "user-work")); err != nil {
					t.Fatalf("create-error cleanup removed work before Stop: %s: %v", dir, err)
				}
			}
			if scenario == "early-hello-late-result" {
				// A late failed result cannot overwrite Stop; importantly the stored
				// image remains empty even though readiness and session work are proven.
				late := event(contract.BoxChanged{From: contract.BoxCreating, To: contract.BoxFailed, Cause: contract.CauseCreateFailed, Phase: contract.CreatePhaseBox})
				if err := st.FinishCreate(ctx, original, "", "host start reply failed", now.Add(time.Minute), 1, late, true); err != nil {
					t.Fatal(err)
				}
			}
			r := lifecycle.New(st, runtime, stopVolumeView{}, nil, func(_ contract.RoomID, _ contract.Principal, body contract.EventBody) (contract.Event, error) {
				return event(body), nil
			}, func() time.Time { return now })
			if err := r.Pass(ctx); err != nil {
				t.Fatal(err)
			}
			r.Wait()
			record, err := st.Box(ctx, "b")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("scenario=%s resulting state=%s image=%q actual commands:\n%s", scenario, record.State, record.Image, raw)
			if record.State != contract.BoxStopped || record.Image != "" {
				t.Fatalf("unexpected Stop result: %+v", record)
			}
			for _, dir := range []string{workspace, stack} {
				if scenario == "first-create-unused" {
					if _, err := os.Stat(filepath.Join(dir, "user-work")); !os.IsNotExist(err) {
						t.Errorf("proven unused first-create volume was not removed: %s: %v", dir, err)
					}
					continue
				}
				if raw, err := os.ReadFile(filepath.Join(dir, "user-work")); err != nil || string(raw) != "must survive Stop" {
					t.Errorf("ordinary Stop erased proven-used named volume %s: %q %v", dir, raw, err)
				}
			}
			if scenario == "first-create-unused" {
				if !strings.Contains(string(raw), "volume rm") || !strings.Contains(string(raw), "volume prune") {
					t.Errorf("unused first create missed workspace/stack cleanup: %s", raw)
				}
			} else if strings.Contains(string(raw), "volume rm") || strings.Contains(string(raw), "volume prune") {
				t.Errorf("ordinary Stop issued destructive named-volume commands after prior readiness and turn")
			}
		})
	}
}

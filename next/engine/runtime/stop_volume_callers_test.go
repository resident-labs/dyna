package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

type beforeReplacementCreate struct{ entered bool }

func (r *beforeReplacementCreate) Get(context.Context, contract.SessionID, contract.HostName, bool) ([]contract.BoxRepo, error) {
	r.entered = true
	return nil, fmt.Errorf("review barrier before replacement Create")
}

// Exercise actual owner restart and recovery callers, including used boxes with empty images.
func TestStartupVolumeCleanupAtRealCallerBoundaries(t *testing.T) {
	for _, scenario := range []string{"first-create-finished", "explicit-restart-used", "hello-during-recovery-remove"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(100000, 0)
			root, tools := t.TempDir(), t.TempDir()
			workspace, stack, logPath := filepath.Join(root, "workspace"), filepath.Join(root, "stack"), filepath.Join(root, "commands")
			for _, dir := range []string{workspace, stack} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "old-work"), []byte("old contents"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			script := `#!/bin/bash
set -eu
printf '%s\n' "$*" >> "$REVIEW_VOLUME_LOG"
if [ -n "${REVIEW_STOP_ENTERED:-}" ] && [ "${2:-}" = stop ]; then
 : > "$REVIEW_STOP_ENTERED"
 while [ ! -f "$REVIEW_STOP_RELEASE" ]; do sleep 0.01; done
fi
case " $* " in
 *' volume rm '*) rm -rf -- "$REVIEW_WORKSPACE" ;;
 *' volume prune '*) rm -rf -- "$REVIEW_STACK" ;;
 *) : ;;
esac
`
			if err := os.WriteFile(filepath.Join(tools, "podman"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tools, "systemctl"), []byte("#!/bin/bash\nexit 0\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
			t.Setenv("REVIEW_VOLUME_LOG", logPath)
			t.Setenv("REVIEW_WORKSPACE", workspace)
			t.Setenv("REVIEW_STACK", stack)
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			entered, release := filepath.Join(root, "stop-entered"), filepath.Join(root, "release-stop")
			if scenario == "hello-during-recovery-remove" {
				t.Setenv("REVIEW_STOP_ENTERED", entered)
				t.Setenv("REVIEW_STOP_RELEASE", release)
				t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
			}
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
			seq := 0
			var eventMu sync.Mutex
			event := func(body contract.EventBody) contract.Event {
				eventMu.Lock()
				defer eventMu.Unlock()
				seq++
				return contract.Event{ID: contract.EventID(fmt.Sprintf("independent-%d", seq)), Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body}
			}
			if err := st.Write(ctx, func(tx *sql.Tx) error {
				_, err := st.AppendEvent(tx, event(contract.BoxChanged{Box: "b", Session: "s", To: contract.BoxPending}))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			original, err := st.Box(ctx, "b")
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "first-create-finished" {
				// The first Create reply persisted its image before any hello or turn.
				if err := st.FinishCreate(ctx, original, "first-image", "", now.Add(time.Minute), 0, event(contract.BoxChanged{}), false); err != nil {
					t.Fatal(err)
				}
				before, err := st.Box(ctx, "b")
				if err != nil || before.State != contract.BoxStarting || before.Image != "first-image" {
					t.Fatalf("before Stop: %+v %v", before, err)
				}
				if _, accepted, err := st.RequestStartStop(ctx, "s", now); err != nil || !accepted {
					t.Fatalf("Stop: %v %v", accepted, err)
				}
			} else if scenario == "explicit-restart-used" {
				ready, err := st.TransitionBox(ctx, original, contract.BoxReady, "", event(contract.BoxChanged{}))
				if err != nil {
					t.Fatal(err)
				}
				failed, err := st.TransitionBox(ctx, ready, contract.BoxFailed, "failed after use", event(contract.BoxChanged{}))
				if err != nil {
					t.Fatal(err)
				}
				_ = failed
				if err := st.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('used','s',1,2,'completed','p',0,0,0,0)`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			sockets := api.NewSockets(nil, t.TempDir(), uint32(os.Getuid()), nil)
			defer sockets.Close()
			h, err := podman.New("user@host", t.TempDir(), "h", &nativePinnedRunner{}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			runtime := &Boxes{Store: st, Sockets: sockets, Layout: box.Layout{Data: root, HostData: root}, Runtime: t.TempDir(), hosts: map[contract.HostName]*podman.Host{"h": h}, restored: map[contract.HostName]bool{"h": true}}
			if scenario == "explicit-restart-used" {
				service := engine.New(st, runtime, nil, func() time.Time { return now })
				if _, problem := service.SessionRestart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionRestartRequest{Session: "s"}); problem != nil {
					t.Fatalf("owner restart: %v", problem)
				}
				// The API must complete both deletions before admission runs.
				for _, dir := range []string{workspace, stack} {
					if _, err := os.Stat(filepath.Join(dir, "old-work")); !os.IsNotExist(err) {
						t.Errorf("owner restart returned before deleting %s: %v", dir, err)
					}
				}
			}
			if scenario == "first-create-finished" {
				r := lifecycle.New(st, runtime, stopVolumeView{}, nil, func(_ contract.RoomID, _ contract.Principal, body contract.EventBody) (contract.Event, error) {
					return event(body), nil
				}, func() time.Time { return now })
				if err := r.Pass(ctx); err != nil {
					t.Fatal(err)
				}
				r.Wait()
				after, err := st.Box(ctx, "b")
				if err != nil || after.State != contract.BoxStopped || after.Image != "" {
					t.Fatalf("after first unused Stop (removed container must not retain an image): %+v %v", after, err)
				}
			} else {
				// Drive healthy-host admission and real runtime Remove. Stop at the
				// repository boundary immediately before the replacement Create.
				repos := &beforeReplacementCreate{}
				r := lifecycle.New(st, runtime, stopVolumeView{}, repos, func(_ contract.RoomID, _ contract.Principal, body contract.EventBody) (contract.Event, error) {
					return event(body), nil
				}, func() time.Time { return now })
				if err := r.Pass(ctx); err != nil {
					t.Fatal(err)
				}
				if scenario == "hello-during-recovery-remove" {
					waitPinnedFile(t, entered)
					// Remove already queried the unused proof, but has not yet killed
					// the surviving pre-restart container. Its reconnect is legal.
					if _, err := st.TransitionBox(ctx, original, contract.BoxReady, "", event(contract.BoxChanged{})); err != nil {
						t.Fatal(err)
					}
					if err := st.Write(ctx, func(tx *sql.Tx) error {
						_, err := tx.Exec(`INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('late-used','s',1,2,'completed','p',0,0,0,0)`)
						return err
					}); err != nil {
						t.Fatal(err)
					}
					// File data is present at the moment typed readiness and work exist.
					for _, dir := range []string{workspace, stack} {
						if _, err := os.Stat(filepath.Join(dir, "old-work")); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.WriteFile(release, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				r.Wait()
				if !repos.entered {
					t.Fatal("restart did not reach the pre-Create repository boundary")
				}
			}
			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("scenario=%s host commands:\n%s", scenario, raw)
			for _, dir := range []string{workspace, stack} {
				_, err := os.Stat(filepath.Join(dir, "old-work"))
				if scenario == "hello-during-recovery-remove" {
					if err != nil {
						t.Errorf("recovery Remove erased box work after readiness and turn: %s: %v", dir, err)
					}
				} else if !os.IsNotExist(err) {
					t.Errorf("%s retained old named volume %s: %v", scenario, dir, err)
				}
			}
			if scenario != "hello-during-recovery-remove" && (!strings.Contains(string(raw), "volume rm") || !strings.Contains(string(raw), "volume prune")) {
				t.Errorf("%s did not issue workspace and stack volume deletion", scenario)
			}
		})
	}
}

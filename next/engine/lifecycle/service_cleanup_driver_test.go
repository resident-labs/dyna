package lifecycle_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman/podmantest"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

// The fixture's real Host runs SSH payloads against local command stubs. These tests begin with removal intent, so no deploy,
// real container or real host is needed to exercise the lifecycle's decision to delete durable state after RemoveService returns.
type cleanupServiceHosts struct{ driver box.ServiceDriver }

func (h cleanupServiceHosts) ServiceDriver(context.Context, contract.HostName) (box.ServiceDriver, error) {
	return h.driver, nil
}
func (cleanupServiceHosts) ServiceRoomImage(context.Context, contract.HostName, contract.RoomID, string) (string, error) {
	return "", errors.New("unexpected image build during cleanup")
}

// The store contains no boxes or sessions. Pass can maintain the host, but no other box or session operation should run.
type cleanupLifecycleBoxes struct{ lifecycle.BoxAdapter }

func (cleanupLifecycleBoxes) MaintainHost(context.Context, contract.HostName) error { return nil }

type cleanupLifecycleView struct{ lifecycle.View }

func (cleanupLifecycleView) OpenTerminal(contract.SessionID) bool { return false }

func TestRemoveServiceCleanupErrorsKeepLifecycleRetryState(t *testing.T) {
	for _, failure := range []string{"secret-list-error", "container-remove-error", "secret-remove-error"} {
		for _, offline := range []bool{false, true} {
			path := "removing_row"
			if offline {
				path = "offline_leftover"
			}
			t.Run(failure+"/"+path, func(t *testing.T) {
				fixture := podmantest.NewServiceCleanup(t)
				ctx := context.Background()
				now := time.Unix(100000, 0)
				dir := t.TempDir()
				st, err := store.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { st.Close() })
				if err := st.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec("INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0)")
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if err := st.AddHost(ctx, box.Host{Name: "h", State: box.HostReady}); err != nil {
					t.Fatal(err)
				}

				var events int
				event := func(room contract.RoomID, body contract.EventBody) (contract.Event, error) {
					events++
					return contract.Event{ID: contract.EventID(fmt.Sprintf("ev_%d", events)), Room: room, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body}, nil
				}
				newReconciler := func() *lifecycle.Reconciler {
					r := lifecycle.New(st, cleanupLifecycleBoxes{}, cleanupLifecycleView{}, nil,
						func(room contract.RoomID, _ contract.Principal, body contract.EventBody) (contract.Event, error) {
							return event(room, body)
						},
						func() time.Time { return now })
					r.SetServices(cleanupServiceHosts{fixture.Host}, lifecycle.DefaultServiceTimings)
					return r
				}
				r := newReconciler()
				pass := func() {
					t.Helper()
					if err := r.Pass(ctx); err != nil {
						t.Fatal(err)
					}
					r.Wait()
				}

				parts, err := (contract.ServiceSpec{Name: "web", Image: "ghcr.io/o/r@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Secrets: []string{"TOKEN"}}).Parse()
				if err != nil {
					t.Fatal(err)
				}
				const id contract.ServiceID = "svc_1"
				if _, err := st.SetService(ctx, "r", parts, id, contract.Principal{Kind: contract.PrincipalOwner}, now, event); err != nil {
					t.Fatal(err)
				}
				if err := st.SetServiceSecret(ctx, id, "TOKEN", []byte("secret value"), now); err != nil {
					t.Fatal(err)
				}
				row, err := st.Service(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				next := row
				next.Host, next.State, next.Deployed, next.Live = "h", contract.ServiceRunning, row.Generation, &parts
				if saved, err := st.SaveService(ctx, row, next, event); err != nil || !saved {
					t.Fatalf("place service: saved=%v err=%v", saved, err)
				}
				if err := r.RemoveService(ctx, id); err != nil {
					t.Fatal(err)
				}
				if offline {
					if err := st.RecordHostHeartbeat(ctx, "h", false, nil, 0, now); err != nil {
						t.Fatal(err)
					}
					pass() // Offline removal deletes the row and secrets, but owes cleanup to this host.
					if _, err := st.Service(ctx, id); !errors.Is(err, store.ErrNotFound) {
						t.Fatalf("offline removal did not delete the row: %v", err)
					}
					debts, err := st.ServiceLeftovers(ctx, "h")
					if err != nil || !slices.Equal(debts, []contract.ServiceID{id}) {
						t.Fatalf("offline removal did not record cleanup debt: %v, %v", debts, err)
					}
					if err := st.RecordHostHeartbeat(ctx, "h", true, nil, 0, now); err != nil {
						t.Fatal(err)
					}
				}

				failureFile := filepath.Join(fixture.Home, failure)
				if err := os.WriteFile(failureFile, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				pass() // The actual driver's payload fails; the lifecycle must not mistake it for complete cleanup.
				assertRetryState := func() {
					t.Helper()
					debts, err := st.ServiceLeftovers(ctx, "h")
					if err != nil {
						t.Fatal(err)
					}
					if offline {
						if !slices.Equal(debts, []contract.ServiceID{id}) {
							t.Fatalf("cleanup failure lost durable retry debt: got %v, want [%s]", debts, id)
						}
					} else {
						row, err := st.Service(ctx, id)
						if err != nil {
							t.Fatalf("cleanup failure lost removing row and its retry state: %v", err)
						}
						if row.Want != contract.ServiceWantRemove || row.State != contract.ServiceRemoving || row.Host != "h" {
							t.Fatalf("cleanup failure changed removal intent: %+v", row.Service)
						}
						values, err := st.ServiceSecretValues(ctx, id)
						if err != nil || string(values["TOKEN"]) != "secret value" {
							t.Fatalf("cleanup failure lost stored secret: %v, %v", values, err)
						}
						if len(debts) != 0 {
							t.Fatalf("ready-host failure should retain the row, not create debt: %v", debts)
						}
					}
				}
				assertRetryState()
				calls, err := os.ReadFile(filepath.Join(fixture.Home, "calls"))
				if err != nil {
					t.Fatal(err)
				}
				command := map[string]string{
					"secret-list-error":      "podman secret ls ",
					"container-remove-error": "podman rm -f ",
					"secret-remove-error":    "podman secret rm ",
				}[failure]
				if !strings.Contains(string(calls), command) {
					t.Fatalf("cleanup never reached failing command %q:\n%s", command, calls)
				}
				remaining := filepath.Join(fixture.Home, "secrets", box.ServiceSecretName(id, "TOKEN"))
				if failure == "container-remove-error" {
					remaining = filepath.Join(fixture.Home, "containers", box.ServiceName(id))
				}
				if _, err := os.Stat(remaining); err != nil {
					t.Fatalf("failure stub did not leave its host object for retry: %v", err)
				}

				// A fresh store handle and reconciler cannot rely on an in-memory retry flag.
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := store.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				st = reopened
				r = newReconciler()
				assertRetryState()
				if err := os.Remove(failureFile); err != nil {
					t.Fatal(err)
				}
				pass()

				if _, err := st.Service(ctx, id); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("successful retry left the service row: %v", err)
				}
				values, err := st.ServiceSecretValues(ctx, id)
				if err != nil || len(values) != 0 {
					t.Fatalf("successful retry left stored secrets: %v, %v", values, err)
				}
				debts, err := st.ServiceLeftovers(ctx, "h")
				if err != nil || len(debts) != 0 {
					t.Fatalf("successful retry left cleanup debt: %v, %v", debts, err)
				}
				for _, path := range fixture.Removed {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Errorf("successful retry left host object %s: %v", path, err)
					}
				}
				for _, path := range fixture.Kept {
					if _, err := os.Stat(path); err != nil {
						t.Errorf("cleanup affected unrelated host object %s: %v", path, err)
					}
				}
			})
		}
	}
}

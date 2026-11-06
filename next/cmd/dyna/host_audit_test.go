package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/spf13/cobra"
)

// hostAuditAPI exposes the real service methods used by these entry paths.
type hostAuditAPI struct {
	api.Service
	core *engine.Service
}

func (a hostAuditAPI) HostSet(ctx context.Context, p contract.Principal, req contract.HostSetRequest) (contract.HostSetResponse, *contract.Error) {
	return a.core.HostSet(ctx, p, req)
}
func (a hostAuditAPI) EventsList(ctx context.Context, p contract.Principal, req contract.EventsListRequest) (contract.EventsListResponse, *contract.Error) {
	return a.core.EventsList(ctx, p, req)
}

// Exercise the web's authenticated owner API and the actual CLI over the owner socket,
// not a second store writer or a fake HostSet implementation.
func TestOwnerHostSwitchAuditEntryPaths(t *testing.T) {
	for _, path := range []string{"web", "cli"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			data := t.TempDir()
			st, err := store.Open(data)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			now := time.Unix(1000, 0).UTC()
			for _, room := range []contract.RoomID{"r1", "r2"} {
				if err := st.CreateRoom(ctx, room, string(room), now); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.AddHost(ctx, box.Host{Name: "build", SSH: "private-user@secret-target", Rooms: []contract.RoomID{"r1"}}); err != nil {
				t.Fatal(err)
			}
			core := engine.New(st, nil, nil, func() time.Time { return now })
			a := api.New(hostAuditAPI{core: core}, nil)
			runtime := filepath.Join(data, "run")
			sockets := api.NewSockets(a, runtime, uint32(os.Getuid()), nil)
			if err := sockets.OpenOwner(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sockets.Close() })
			httpClient := unixHTTP(filepath.Join(runtime, "owner.sock"))
			t.Cleanup(httpClient.CloseIdleConnections)
			c := &socketClient{http: httpClient}
			cli := func(args ...string) string {
				t.Helper()
				root := &cobra.Command{Use: "dyna", SilenceUsage: true, SilenceErrors: true}
				var out bytes.Buffer
				root.SetOut(&out)
				root.SetErr(&out)
				addCLI(root, func() client { return c })
				root.SetArgs(args)
				if err := root.Execute(); err != nil {
					t.Fatal(err)
				}
				return out.String()
			}
			web, err := api.NewWeb(a, "127.0.0.1:7790", fstest.MapFS{}, func() time.Time { return now }, st)
			if err != nil {
				t.Fatal(err)
			}
			const token = "private-web-cookie-token"
			owner := contract.Principal{Kind: contract.PrincipalOwner}
			if path == "web" {
				owner.Device = "web-device"
			}
			if err := st.CreateDevice(ctx, store.Device{ID: "web-device", Name: "Test web", TokenHash: sha256.Sum256([]byte(token)), PairedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour), Scopes: store.FullDeviceScopes}); err != nil {
				t.Fatal(err)
			}
			on, off := true, false
			var want []map[string]any
			for _, step := range []struct {
				args []string
				req  contract.HostSetRequest
				body map[string]any
			}{
				{[]string{"--ci", "on"}, contract.HostSetRequest{CI: &on}, map[string]any{"host": "build", "ci": true}},
				{[]string{"--ci", "on"}, contract.HostSetRequest{CI: &on}, nil},
				{[]string{"--agents", "off", "--ci", "on"}, contract.HostSetRequest{Agents: &off, CI: &on}, map[string]any{"host": "build", "agents": false}},
				{[]string{"--agents", "on", "--ci", "off"}, contract.HostSetRequest{Agents: &on, CI: &off}, map[string]any{"host": "build", "agents": true, "ci": false}},
				{[]string{"--clear-create-problem"}, contract.HostSetRequest{ClearCreateProblem: true}, nil},
			} {
				if path == "cli" {
					cli(append([]string{"host", "set", "build"}, step.args...)...)
				} else {
					step.req.Host = "build"
					body, err := json.Marshal(step.req)
					if err != nil {
						t.Fatal(err)
					}
					r := httptest.NewRequest(http.MethodPost, "http://dyna.localhost:7790/v1/host.set", bytes.NewReader(body))
					r.Header.Set("Origin", "http://dyna.localhost:7790")
					r.Header.Set("Content-Type", "application/json")
					r.AddCookie(&http.Cookie{Name: "dyna_device", Value: token})
					out := httptest.NewRecorder()
					web.ServeHTTP(out, r)
					if out.Code != http.StatusOK {
						t.Fatalf("host.set: %d %s", out.Code, out.Body.String())
					}
				}
				if step.body != nil {
					want = append(want, step.body)
				}
				for _, room := range []contract.RoomID{"r1", "r2"} {
					list, err := c.EventsList(ctx, contract.EventsListRequest{Room: room, Limit: 100})
					if err != nil {
						t.Fatal(err)
					}
					if len(list.Events) != len(want) {
						t.Fatalf("%s room %s after %v: got %d events, want %d", path, room, step.args, len(list.Events), len(want))
					}
					for i, event := range list.Events {
						if event.Body.EventType() != "host.switches_changed" || event.From != owner || event.Room != room || !event.Time.Equal(now) {
							t.Fatalf("audit envelope: %+v", event)
						}
						body, err := json.Marshal(event.Body)
						if err != nil {
							t.Fatal(err)
						}
						var got map[string]any
						if err := json.Unmarshal(body, &got); err != nil || !reflect.DeepEqual(got, want[i]) {
							t.Fatalf("audit body: %s, want %+v (%v)", body, want[i], err)
						}
					}
				}
			}
			// The CLI room log replays the typed event, including the owner, without connection secrets.
			log := cli("room", "log", "r1")
			if strings.Count(log, `"type":"host.switches_changed"`) != len(want) || !strings.Contains(log, `"kind":"owner"`) {
				t.Fatalf("room log: %s", log)
			}
			for _, secret := range []string{"private-user", "secret-target", token} {
				if strings.Contains(log, secret) {
					t.Fatalf("room log leaks %q", secret)
				}
			}
		})
	}
}

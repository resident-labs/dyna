//go:build linux

package computers

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"dyna-agent/internal/websvc/devicestore"
)

// fakeBrowser serves the Chromium endpoint of id and sends each Storage.setCookies token to the returned channel.
func fakeBrowser(t *testing.T, id string) <-chan string {
	t.Helper()
	tokens := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var call struct {
			ID     int
			Method string
			Params struct {
				Cookies []struct{ Name, Value string }
			}
		}
		if conn.ReadJSON(&call) != nil {
			return
		}
		if call.Method == "Storage.setCookies" && len(call.Params.Cookies) == 1 && call.Params.Cookies[0].Name == devicestore.CookieName {
			tokens <- call.Params.Cookies[0].Value
		}
		_ = conn.WriteJSON(map[string]any{"id": call.ID, "result": map[string]any{}})
	}))
	t.Cleanup(server.Close)
	addr := server.Listener.Addr().String()
	port := addr[strings.LastIndex(addr, ":")+1:]
	if err := os.MkdirAll(computerBrowserProfile(id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(computerBrowserProfile(id), "DevToolsActivePort"), []byte(port+"\n/devtools/browser/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return tokens
}

func browserDevices(t *testing.T) *devicestore.Store {
	t.Helper()
	devices, err := devicestore.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	return devices
}

func TestSignInBrowserPairsShortLivedNamedDevice(t *testing.T) {
	browserTestEnvironment(t)
	id := "ses_20261019-193526-75c8bf70"
	tokens := fakeBrowser(t, id)
	if err := signInBrowser(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	token := <-tokens
	dev, ok := browserDevices(t).Check(token)
	if !ok {
		t.Fatal("the cookie token is not a live device")
	}
	if dev.Name != "dyna browser · "+id[len(id)-24:] {
		t.Fatalf("device name %q", dev.Name)
	}
	if life := time.Until(dev.Expires); life <= 0 || life > browserDeviceLife {
		t.Fatalf("device lives %s, want at most %s", life, browserDeviceLife)
	}
	// A second start replaces the device instead of adding one.
	if err := signInBrowser(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if list, _ := browserDevices(t).Devices(); len(list) != 1 {
		t.Fatalf("devices after second sign-in: %+v", list)
	}
}

func TestStopRevokesOnlyThisBrowsersDevice(t *testing.T) {
	browserTestEnvironment(t)
	for _, id := range []string{"a-session", "b-session"} {
		fakeBrowser(t, id)
		if err := signInBrowser(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	stopProfileBrowsers("a-session")
	list, _ := browserDevices(t).Devices()
	if len(list) != 1 || list[0].ID != browserDeviceID("b-session") {
		t.Fatalf("live devices after stop: %+v", list)
	}
}

func TestLateExitDoesNotRevokeNewerDevice(t *testing.T) {
	browserTestEnvironment(t)
	id := "late-exit"
	fakeBrowser(t, id)
	if err := signInBrowser(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	revokeBrowserDevice(id, time.Now().Add(-time.Hour))
	if list, _ := browserDevices(t).Devices(); len(list) != 1 {
		t.Fatalf("an older browser's exit revoked a newer device: %+v", list)
	}
}

// A browser started through the shared launcher, not by the computer, still loses its device when it exits.
func TestBrowserExitUnderLauncherRevokesDevice(t *testing.T) {
	browserTestEnvironment(t)
	id := "shim-exit"
	if err := os.MkdirAll(filepath.Dir(ControlSocket(id)), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", ControlSocket(id))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executable := filepath.Join(t.TempDir(), "browser")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	fake := NewFakeSupervisor(id)
	fake.Handle = func(_ context.Context, r Request) (any, error) {
		return chromiumLaunch{Executable: executable, Args: r.BrowserArgs}, nil
	}
	go fake.Serve(ctx, listener)
	fakeBrowser(t, id)
	if err := signInBrowser(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := LaunchChromium(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if list, _ := browserDevices(t).Devices(); len(list) != 0 {
		t.Fatalf("device survived browser exit: %+v", list)
	}
}

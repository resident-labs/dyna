package main

import (
	"dyna-agent/internal/scope"
	"dyna-agent/internal/websvc/devicestore"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestDesktopLoadsServerWithPairingCode(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	page := desktopPage(strings.TrimPrefix(server.URL, "http://"))
	match := regexp.MustCompile(`#pair=([A-Z2-9-]+)`).FindStringSubmatch(page)
	if len(match) != 2 || !strings.Contains(page, server.URL+"/") {
		t.Fatalf("window did not load the server with a pairing code: %s", page)
	}
	store, err := devicestore.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	_, device, err := store.Pair(match[1], "Dyna desktop")
	if err != nil || device.ID != "desktop" || !reflect.DeepEqual(device.Scopes, scope.All()) {
		t.Fatalf("desktop pairing = %+v, %v", device, err)
	}
}

func TestDesktopShowsStartCommandWhenServerIsDown(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	page := desktopPage(strings.TrimPrefix(server.URL, "http://"))
	if !strings.Contains(page, "systemctl --user start dyna-engine") || strings.Contains(page, "location.replace") {
		t.Fatalf("missing server message: %s", page)
	}
}

func TestDesktopRejectsNonLoopbackAddress(t *testing.T) {
	page := desktopPage("192.0.2.1:7457")
	if !strings.Contains(page, "not a loopback address") || strings.Contains(page, "location.replace") {
		t.Fatalf("accepted a remote server: %s", page)
	}
}

func TestDesktopShowsStartCommandForServerError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	u, _ := url.Parse(server.URL)
	if page := desktopPage(u.Host); !strings.Contains(page, "systemctl --user start dyna-engine") {
		t.Fatalf("missing server message: %s", page)
	}
}

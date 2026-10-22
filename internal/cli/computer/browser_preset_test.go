package computer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeCDPBrowser accepts one connection, answers every command, and announces one paused tab after Target.setAutoAttach.
// Every command it receives is sent on the returned channel as "method session".
func fakeCDPBrowser(t *testing.T) (endpoint string, commands <-chan string) {
	t.Helper()
	out := make(chan string, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var msg struct {
				ID        int            `json:"id"`
				Method    string         `json:"method"`
				Params    map[string]any `json:"params"`
				SessionID string         `json:"sessionId"`
			}
			if conn.ReadJSON(&msg) != nil {
				return
			}
			params, _ := json.Marshal(msg.Params)
			out <- msg.Method + " " + msg.SessionID + " " + string(params)
			conn.WriteJSON(map[string]any{"id": msg.ID, "result": map[string]any{}})
			if msg.Method == "Target.setAutoAttach" {
				conn.WriteJSON(map[string]any{"method": "Target.attachedToTarget", "params": map[string]any{
					"sessionId": "tab1", "waitingForDebugger": true, "targetInfo": map[string]any{"type": "page", "url": "http://example.test/"}}})
			}
		}
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/id", out
}

func TestBrowserPresetPhoneEmulatesAMobilePageUntilOff(t *testing.T) {
	endpoint, commands := fakeCDPBrowser(t)
	call := nodeBrowserFixture(t, endpoint)
	id, _, err := resolveActor()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call("preset", "tablet"); err == nil {
		t.Fatal("unknown preset accepted")
	}
	if out, err := call("preset", "phone", "--options"); err != nil || !strings.Contains(out, `{"viewport":{"width":390,"height":844},"deviceScaleFactor":3,"isMobile":true,"hasTouch":true,"userAgent":"Mozilla/5.0 (Linux; Android 14`) {
		t.Fatalf("options: %q %v", out, err)
	}
	if holderPID(holderDir(id, "preset")) != 0 {
		t.Fatal("--options started a holder")
	}
	if out, err := call("preset", "phone"); err != nil || !strings.Contains(out, "preset phone on") {
		t.Fatalf("phone: %q %v", out, err)
	}
	// The paused tab gets the phone settings, is resumed, then reloaded, in that order.
	var seen []string
	for len(seen) < 6 {
		select {
		case c := <-commands:
			seen = append(seen, c)
		case <-time.After(5 * time.Second):
			t.Fatalf("commands so far: %v", seen)
		}
	}
	want := []string{
		`Target.setAutoAttach  {"autoAttach":true,"flatten":true,"waitForDebuggerOnStart":true}`,
		`Emulation.setDeviceMetricsOverride tab1 {"deviceScaleFactor":3,"height":844,"mobile":true,"screenHeight":844,"screenWidth":390,"width":390}`,
		`Emulation.setTouchEmulationEnabled tab1 {"enabled":true,"maxTouchPoints":5}`,
		`Emulation.setEmitTouchEventsForMouse tab1 {"configuration":"mobile","enabled":true}`,
	}
	for i, w := range want {
		if seen[i] != w {
			t.Fatalf("command %d = %s, want %s", i, seen[i], w)
		}
	}
	if !strings.HasPrefix(seen[4], "Emulation.setUserAgentOverride tab1 ") || !strings.Contains(seen[4], "Android 14; Pixel 8") || seen[5] != "Runtime.runIfWaitingForDebugger tab1 {}" {
		t.Fatalf("user agent and resume: %v", seen[4:])
	}
	if holderPID(holderDir(id, "preset")) == 0 {
		t.Fatal("phone holder is not running")
	}
	if out, err := call("preset", "off"); err != nil || !strings.Contains(out, "preset off") {
		t.Fatalf("off: %q %v", out, err)
	}
	if holderPID(holderDir(id, "preset")) != 0 {
		t.Fatal("holder still running after off")
	}
}

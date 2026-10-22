package connect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLinkTokenFailureRecoversWithSync(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	machine := MachineInfo{ID: "m_test", Name: "test", Owner: Owner{ID: "owner", Name: "Owner"}, Epoch: 1}
	tokenCalls := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/link/start":
			json.NewEncoder(w).Encode(LinkStart{ID: "link_test", Expires: time.Now().Add(time.Minute).UnixMilli()})
		case "/v1/link/poll":
			json.NewEncoder(w).Encode(LinkPoll{Status: "linked", Machine: machine})
		case "/v1/machine/tunnel-token":
			tokenCalls++
			if tokenCalls == 1 {
				http.Error(w, "unavailable", http.StatusBadGateway)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"token": "recovered-token"})
		case "/v1/machine/status":
			json.NewEncoder(w).Encode(map[string]any{"machine": machine})
		case "/v1/machine/origin", "/v1/machine/routes":
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer relay.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = st.linkMachine(ctx, LinkOptions{Relay: relay.URL, Name: "test", AppPort: 7457, CardPort: 7458}, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "engine run tries again") {
		t.Fatalf("link error = %v", err)
	}
	l, ok, err := st.Link()
	if err != nil || !ok || l.Machine != machine.ID || l.Relay != relay.URL {
		t.Fatalf("saved link = %+v, %v, %v", l, ok, err)
	}
	svc, err := New(Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Sync(ctx, 7457, 7458); err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(st.TunnelTokenPath())
	if err != nil || string(token) != "recovered-token" || tokenCalls != 2 {
		t.Fatalf("token = %q, error = %v, calls = %d", token, err, tokenCalls)
	}
}

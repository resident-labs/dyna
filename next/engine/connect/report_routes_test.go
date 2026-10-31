package connect

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReportRoutesFiltersAndLimitsRelayTargets(t *testing.T) {
	var got struct {
		Machine string          `json:"machine"`
		Routes  []reportedRoute `json:"routes"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/machine/routes" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	relay := Relay{Base: server.URL, Key: key}
	link := Link{Machine: "invented", App: "https://tunnel.example", Cards: "https://cards.example"}
	origins := []string{link.App, "http://dyna.localhost:8080", "http://127.0.0.2:8080", "http://localhost:8080", "https://node.ts.net",
		"https://a.example", "https://b.example", "https://c.example", "https://d.example", "https://e.example", "https://f.example", "https://g.example"}
	if err := relay.reportRoutes(context.Background(), link, origins); err != nil {
		t.Fatal(err)
	}
	if got.Machine != link.Machine || len(got.Routes) != 8 {
		t.Fatalf("reported routes: %+v", got)
	}
	if got.Routes[0].Kind != "loopback" || got.Routes[0].App != "http://localhost:8080" || got.Routes[1].Kind != "tailscale" {
		t.Fatalf("route order: %+v", got.Routes)
	}
	for _, route := range got.Routes {
		if route.Cards != link.Cards || route.App == "http://dyna.localhost:8080" || route.App == "http://127.0.0.2:8080" || route.App == link.App {
			t.Fatalf("unexpected route: %+v", route)
		}
	}
}

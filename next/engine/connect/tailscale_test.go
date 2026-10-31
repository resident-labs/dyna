package connect

import "testing"

func TestTailscaleServeDiscovery(t *testing.T) {
	status := []byte(`{"Web":{"desk.example.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7457"}}}}}`)
	if got, err := parseTailscaleServe(status, 7457); err != nil || got != "https://desk.example.ts.net" {
		t.Fatalf("route = %q", got)
	}
	if got, err := parseTailscaleServe(status, 9999); err != nil || got != "" {
		t.Fatalf("accepted a different app port: %q", got)
	}
	other := []byte(`{"Web":{"desk.example.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://localhost:7457/"}}}}}`)
	if got, err := parseTailscaleServe(other, 7457); err != nil || got != "https://desk.example.ts.net:8443" {
		t.Fatalf("non-default port: %q", got)
	}
	if got, err := parseTailscaleServe([]byte(`{"Web":{"host.ts.net:443":{"Handlers":{"/other":{"Proxy":"http://127.0.0.1:7457"}}}}}`), 7457); err != nil || got != "" {
		t.Fatalf("accepted non-root mapping: %q", got)
	}
	if _, err := parseTailscaleServe([]byte("{"), 7457); err == nil {
		t.Fatal("accepted invalid JSON")
	}
}

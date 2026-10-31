package connect

import "testing"

func TestTailscaleServeDiscovery(t *testing.T) {
	status := []byte(`{"Web":{"desk.example.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7457"}}}}}`)
	if got := parseTailscaleServe(status, 7457); got != "https://desk.example.ts.net" {
		t.Fatalf("route = %q", got)
	}
	if got := parseTailscaleServe(status, 9999); got != "" {
		t.Fatalf("accepted a different app port: %q", got)
	}
	other := []byte(`{"Web":{"desk.example.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://localhost:7457/"}}}}}`)
	if got := parseTailscaleServe(other, 7457); got != "https://desk.example.ts.net:8443" {
		t.Fatalf("non-default port: %q", got)
	}
	if got := parseTailscaleServe([]byte(`{"Web":{"host.ts.net:443":{"Handlers":{"/other":{"Proxy":"http://127.0.0.1:7457"}}}}}`), 7457); got != "" {
		t.Fatalf("accepted non-root mapping: %q", got)
	}
}

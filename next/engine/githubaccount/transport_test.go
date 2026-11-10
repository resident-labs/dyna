package githubaccount

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransportLabelsRejectionWithoutRetryOrSecret(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer work-secret" {
			t.Error("wrong identity")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials work-secret","detail":"work-secret"}`))
	}))
	defer server.Close()
	client := &http.Client{Transport: Transport{Base: server.Client().Transport, Label: "GitHub account work of room itasca", Token: "work-secret"}}
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	req.Header.Set("Authorization", "Bearer work-secret")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || response.StatusCode != 401 || !strings.Contains(string(data), "GitHub account work of room itasca: Bad credentials") || strings.Contains(string(data), "work-secret") {
		t.Fatalf("unsafe rejection: %d %s", calls, data)
	}
}

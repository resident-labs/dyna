package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQuotaClearFencesInflightRead(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dyna/credentials" {
			_, _ = w.Write([]byte(`{"credentials":[{"id":"invented","auth_index":"index","provider":"claude","state":"unknown"}]}`))
			return
		}
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"status_code":401,"body":"{}"}`))
	}))
	defer server.Close()
	q := (&Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}).Quotas("key", quotaStore(t), time.Now)
	done := make(chan struct{})
	go func() { _, _ = q.Read(context.Background()); close(done) }()
	<-started
	q.Clear()
	close(release)
	<-done
	if q.ObservedState("index") != CredentialUnknown {
		t.Fatal("stale usage changed the new account state")
	}
}

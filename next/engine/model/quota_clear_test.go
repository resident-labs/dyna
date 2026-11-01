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
		_, _ = w.Write([]byte(`{"files":[{"auth_index":"index","status_code":401}]}`))
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

func TestPassiveQuotaLeavesAccountStateUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"credentials":[{"id":"invented","auth_index":"index","provider":"kimi","state":"unknown"}]}`))
	}))
	defer server.Close()
	q := (&Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}).Quotas("key", quotaStore(t), time.Now)
	if _, err := q.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.CachedUsage("account"); !ok {
		t.Fatal("search cannot read passive usage")
	}
	if state := q.ObservedState("index"); state != CredentialUnknown {
		t.Fatalf("state = %s", state)
	}
}

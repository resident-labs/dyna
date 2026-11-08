package model

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStreamWireIdle(t *testing.T) {
	if streamIdleTimeout != 300*time.Second {
		t.Fatal(streamIdleTimeout)
	}
	for _, mode := range []string{"headers", "body", "keepalive", "cancel", "ended"} {
		t.Run(mode, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "s")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "headers" {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				if mode == "ended" {
					return
				}
				if mode == "body" {
					_, _ = io.WriteString(w, "data: first event\n\n")
					w.(http.Flusher).Flush()
				}
				if mode == "keepalive" {
					for range 15 {
						_, _ = io.WriteString(w, ": ping\n\n")
						w.(http.Flusher).Flush()
						time.Sleep(20 * time.Millisecond)
					}
					return
				}
				<-r.Context().Done()
			})}
			go server.Serve(listener)
			defer server.Close()
			client := &http.Client{Transport: idleTransport(socket, 200*time.Millisecond)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://proxy/", nil)
			start := time.Now()
			resp, err := client.Do(req)
			if err == nil {
				if mode == "cancel" {
					cancel()
				}
				_, err = io.ReadAll(resp.Body)
				resp.Body.Close()
			}
			elapsed := time.Since(start)
			if mode == "keepalive" || mode == "ended" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("silent request succeeded")
			}
			if elapsed > 2*time.Second {
				t.Fatalf("request did not stop promptly: %s", elapsed)
			}
		})
	}
}

func TestCancelBlockedModelUpload(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "upload")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{})
	release := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		<-release
	}()
	defer listener.Close()
	defer close(release)
	client := &http.Client{Transport: idleTransport(socket, streamIdleTimeout)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://proxy/", strings.NewReader(strings.Repeat("x", 8<<20)))
	done := make(chan error, 1)
	go func() {
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()
	<-accepted
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled upload succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not interrupt blocked upload")
	}
	t.Logf("blocked upload cancellation: %s", time.Since(start))
}

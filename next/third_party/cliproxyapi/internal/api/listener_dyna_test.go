package api

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type suppliedListener struct {
	conn chan net.Conn
	done chan struct{}
	once sync.Once
}

func (l *suppliedListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conn:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *suppliedListener) Close() error { l.once.Do(func() { close(l.done) }); return nil }
func (l *suppliedListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}
}

func TestDynaSuppliedListener(t *testing.T) {
	listener := &suppliedListener{conn: make(chan net.Conn), done: make(chan struct{})}
	server := &Server{listener: listener, server: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "served") })}}
	result := make(chan error, 1)
	go func() { result <- server.Start() }()
	client, peer := net.Pipe()
	listener.conn <- peer
	go func() {
		_, _ = io.WriteString(client, "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
	}()
	resp, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || strings.TrimSpace(string(body)) != "served" {
		t.Fatalf("body %q: %v", body, err)
	}
	_ = resp.Body.Close()
	_ = client.Close()
	_ = server.Stop(context.Background())
	<-result
}

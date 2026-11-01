package share

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func serveEcho(t *testing.T, path string) net.Listener {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener
}

func TestRelayWaitsForAServerThatIsAway(t *testing.T) {
	dir := t.TempDir()
	listen, server := filepath.Join(dir, "relay.sock"), filepath.Join(dir, "server.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := relay(ctx, listen, server, time.Minute); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * relayRetry)
	serveEcho(t, server)
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	got := make([]byte, 5)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "hello" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestRelayGivesUpOnAServerThatStaysAway(t *testing.T) {
	dir := t.TempDir()
	listen := filepath.Join(dir, "relay.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := relay(ctx, listen, filepath.Join(dir, "server.sock"), 0); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("err = %v, want the caller told at once", err)
	}
}

func TestRelayReplacesASocketLeftBehind(t *testing.T) {
	dir := t.TempDir()
	listen := filepath.Join(dir, "relay.sock")
	first, cancelFirst := context.WithCancel(context.Background())
	if err := relay(first, listen, filepath.Join(dir, "server.sock"), 0); err != nil {
		t.Fatal(err)
	}
	cancelFirst()
	second, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	if err := relay(second, listen, filepath.Join(dir, "server.sock"), 0); err != nil {
		t.Fatal(err)
	}
}

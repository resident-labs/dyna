package computers

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildServer answers one connection with reply, then closes it. An empty
// reply closes without answering.
func buildServer(t *testing.T, reply string) *Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "bld")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 512)
		_, _ = conn.Read(buf)
		if reply != "" {
			_, _ = conn.Write([]byte(reply + "\n"))
		}
	}()
	return &Client{Socket: socket}
}

func TestClientBuildKeepsFailuresApart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if build, err := buildServer(t, `"dev+x"`).Build(ctx); err != nil || build != "dev+x" {
		t.Fatalf("build = %q, %v", build, err)
	}
	// A daemon from before the version call answers with an error.
	if _, err := buildServer(t, `{"error":"invalid session id"}`).Build(ctx); !errors.Is(err, ErrUnknownBuild) {
		t.Fatalf("legacy daemon: %v, want ErrUnknownBuild", err)
	}
	// A connection that closes without an answer says nothing about the build.
	for name, client := range map[string]*Client{
		"closed":    buildServer(t, ""),
		"malformed": buildServer(t, `{"error"`),
	} {
		_, err := client.Build(ctx)
		if err == nil || errors.Is(err, ErrUnknownBuild) || errors.Is(err, ErrNoDaemon) {
			t.Fatalf("%s: %v, want the transport error", name, err)
		}
	}
	if _, err := (&Client{Socket: filepath.Join(t.TempDir(), "none")}).Build(ctx); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("no socket: %v, want ErrNoDaemon", err)
	}
}

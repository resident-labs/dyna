package connect

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A new link or an unlink takes effect without an engine restart: a changed
// token restarts the connector, and a removed one leaves none running.
func TestTunnelFollowsItsCredential(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTunnelToken("first"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mark := filepath.Join(dir, "starts")
	bin := filepath.Join(dir, "cloudflared")
	script := "#!/bin/sh\necho start >> " + mark + "\nexec sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := tokenWatchInterval
	tokenWatchInterval = 20 * time.Millisecond
	t.Cleanup(func() { tokenWatchInterval = previous })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunTunnel(ctx, st, bin, t.Logf)
	if err := waitForStarts(mark, 1); err != nil {
		t.Fatal(err)
	}
	// A new link writes a new token, so the child holding the old tunnel stops
	// and a second one starts with the new credential.
	if err := st.SaveTunnelToken("second"); err != nil {
		t.Fatal(err)
	}
	if err := waitForStarts(mark, 2); err != nil {
		t.Fatalf("the connector did not follow a new token: %v", err)
	}
	// An unlink removes the token, and no further connector starts.
	if err := os.Remove(st.TunnelTokenPath()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if starts, err := countStarts(mark); err != nil || starts != 2 {
		t.Fatalf("starts after unlink = %d, %v; want 2", starts, err)
	}
}

// waitForStarts waits until the fake connector has started that many times.
func waitForStarts(mark string, want int) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if starts, err := countStarts(mark); err == nil && starts >= want {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("the connector started %d times, want %d", count(mark), want)
}

func count(mark string) int {
	starts, _ := countStarts(mark)
	return starts
}

func countStarts(mark string) (int, error) {
	body, err := os.ReadFile(mark)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strings.Count(string(body), "start"), nil
}

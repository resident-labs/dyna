package engine

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/store/storetest"
)

func TestHealthServesWhileMigrationBlocks(t *testing.T) {
	restoreAuthority := authority.InEngine()
	restoreAuthority()
	t.Cleanup(restoreAuthority)
	storetest.DataDir(t)
	runtimeDir, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	savedMigration, savedReady := migrateStores, fullAPIReady
	t.Cleanup(func() { migrateStores, fullAPIReady = savedMigration, savedReady })
	migrationStarted := make(chan struct{})
	releaseMigration := make(chan struct{})
	migrateStores = func(dir string) error {
		close(migrationStarted)
		<-releaseMigration
		return savedMigration(dir)
	}
	ready := make(chan struct{})
	fullAPIReady = func() { close(ready) }

	addresses := make([]string, 2)
	for i := range addresses {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addresses[i] = listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, Options{Addr: addresses[0], CardAddr: addresses[1], NoTunnel: true, NoTailscale: true, NoPush: true}, true)
	}()
	select {
	case <-migrationStarted:
	case err := <-done:
		t.Fatalf("engine exited before migration blocked: %v", err)
	}

	client := engineclient.New("", engineclient.RestartWait(0), engineclient.Notices(nil))
	var health Health
	if err := client.Call(ctx, "Health", nil, &health); err != nil {
		t.Fatalf("Health did not answer during migration: %v", err)
	}
	if health.Status != healthStarting || health.Ready {
		t.Fatalf("Health during migration = %+v", health)
	}
	if err := client.Call(ctx, "Rooms", nil, nil); err == nil {
		t.Fatal("an ordinary API call ran before migration finished")
	}

	close(releaseMigration)
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("engine exited before becoming ready: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

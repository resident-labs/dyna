package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A failed update must leave the selected and pinned copy usable. A successful
// update changes the default only after its complete bundle is published.
func TestPrivateRuntimeUpdatePublishesBeforeSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := testPrivateRuntime(t)
	m := a.Bundles
	t.Cleanup(func() { _ = removeBundle(m.Root) })
	m.Run = fixtureInstall
	old := install(t, m, "codex", "1.0.0")
	if err := m.Select("codex", old.Version); err != nil {
		t.Fatal(err)
	}
	pinned, err := os.Open(old.Executable)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	oldInfo, _ := pinned.Stat()
	release, err := m.Lease(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	started, finish := make(chan struct{}), make(chan struct{})
	m.Run = func(ctx context.Context, args, env []string) error {
		if err := fixtureInstall(ctx, args, env); err != nil {
			return err
		}
		close(started)
		select {
		case <-finish:
		case <-ctx.Done():
			return ctx.Err()
		}
		return errors.New("download failed after extraction")
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _, err := a.InstallPrivateRuntime(ctx, "codex", "2.0.0"); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("installer did not start")
	}
	got, err := m.Resolve("codex", "")
	if err != nil || got != old {
		t.Fatal("selection changed during download", got, err)
	}
	if _, err := m.Resolve("codex", "2.0.0"); err == nil {
		t.Fatal("partial bundle was published")
	}
	close(finish)
	if err := <-done; err == nil {
		t.Fatal("failed download succeeded")
	}
	got, err = m.Resolve("codex", "")
	if err != nil || got != old {
		t.Fatal("failure changed selection", got, err)
	}
	entries, err := os.ReadDir(filepath.Join(m.Root, "codex"))
	if err != nil || len(entries) != 1 {
		t.Fatal("failed download left staging files", entries, err)
	}
	m.Run = fixtureInstall
	snapshot, err := a.InstallPrivateRuntime(context.Background(), "codex", "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SelectedVersions["codex"] != "2.0.0" {
		t.Fatal("update did not select installed version", snapshot.SelectedVersions)
	}
	got, err = m.Resolve("codex", old.Version)
	if err != nil || got != old {
		t.Fatal("update changed the pinned bundle", got, err)
	}
	currentInfo, err := os.Stat(old.Executable)
	if err != nil || !os.SameFile(oldInfo, currentInfo) {
		t.Fatal("update replaced the executable in use", err)
	}
}

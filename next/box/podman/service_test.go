package podman_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box/podman/podmantest"
)

func TestRemoveServiceCleansOnlyItsObjectsAndIsIdempotent(t *testing.T) {
	f := podmantest.NewServiceCleanup(t)
	for attempt := range 2 {
		if err := f.Host.RemoveService(context.Background(), "svc_1"); err != nil {
			t.Errorf("remove attempt %d: %v", attempt+1, err)
		}
		for _, path := range f.Removed {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("remove attempt %d left %s: %v", attempt+1, path, err)
			}
		}
		for _, path := range f.Kept {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("remove attempt %d affected unrelated %s: %v", attempt+1, path, err)
			}
		}
	}
	calls, err := os.ReadFile(filepath.Join(f.Home, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"systemctl --user stop dyna-svc-svc_1.service", "systemctl --user daemon-reload", "podman rm -f --ignore dyna-svc-svc_1", "podman secret rm dyna-svc-svc_1-TOKEN", "podman secret rm dyna-svc-svc_1-KEY"} {
		if !strings.Contains(string(calls), call+"\n") {
			t.Errorf("missing call %q in:\n%s", call, calls)
		}
	}
}

func TestRemoveServicePropagatesCleanupFailuresAndCanRetry(t *testing.T) {
	for _, mode := range []string{"secret-list-error", "container-remove-error", "secret-remove-error", "unit-stop-error", "file-remove-error", "reload-error", "volume-remove-error"} {
		t.Run(mode, func(t *testing.T) {
			f := podmantest.NewServiceCleanup(t)
			failure := filepath.Join(f.Home, mode)
			if err := os.WriteFile(failure, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := f.Host.RemoveService(context.Background(), "svc_1"); err == nil {
				t.Errorf("%s was swallowed by the actual cleanup payload", mode)
			}
			if err := os.Remove(failure); err != nil {
				t.Fatal(err)
			}
			if err := f.Host.RemoveService(context.Background(), "svc_1"); err != nil {
				t.Fatalf("retry after %s: %v", mode, err)
			}
			for _, path := range f.Removed {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("successful retry left %s: %v", path, err)
				}
			}
			for _, path := range f.Kept {
				if _, err := os.Stat(path); err != nil {
					t.Errorf("cleanup affected unrelated %s: %v", path, err)
				}
			}
		})
	}
}

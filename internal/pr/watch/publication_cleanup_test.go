package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationGateRemovesOnlyInactiveFiles(t *testing.T) {
	e := newEnv(t)
	idle, owned, err := e.s.tryPublicationGate("idle")
	if err != nil || !owned {
		t.Fatalf("idle gate: owned=%v, err=%v", owned, err)
	}
	idle()
	idlePath := filepath.Join(e.s.Store.Root, "publication-"+digest("idle")+".lock")
	if _, err := os.Stat(idlePath); !os.IsNotExist(err) {
		t.Errorf("released publication left a lock file: %v", err)
	}
	// An older release can leave an idle file behind.
	if err := os.WriteFile(idlePath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	active, owned, err := e.s.tryPublicationGate("active")
	if err != nil || !owned {
		t.Fatalf("active gate: owned=%v, err=%v", owned, err)
	}
	defer active()
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(idlePath); !os.IsNotExist(err) {
		t.Errorf("idle lock survived tick: %v", err)
	}
	if _, owned, err := e.s.tryPublicationGate("active"); err != nil || owned {
		t.Fatalf("cleanup removed active gate: owned=%v, err=%v", owned, err)
	}
}

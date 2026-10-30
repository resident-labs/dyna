package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestPiPackageInstalledBesideBinary(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "dyna")
	path := filepath.Join(dir, "pi.tgz")
	if _, err := loadPi(executable); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("missing package error: %v", err)
	}
	if err := os.WriteFile(path, []byte("pi"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := loadPi(executable)
	if err != nil || string(data) != "pi" {
		t.Fatalf("package %q, error %v", data, err)
	}
}

type fakeRegistrar struct{ called bool }

func (f *fakeRegistrar) AddHost(context.Context, contract.HostAddRequest) error {
	f.called = true
	return nil
}

func TestHostAddRequiresOwnerAndConfirmation(t *testing.T) {
	f := &fakeRegistrar{}
	s := serviceWithHosts{boxes: f}
	_, err := s.HostAdd(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, contract.HostAddRequest{Name: "host", SSH: "user@host"})
	if err == nil || err.Code != contract.ErrInvalid || f.called {
		t.Fatalf("error=%v called=%v", err, f.called)
	}
}

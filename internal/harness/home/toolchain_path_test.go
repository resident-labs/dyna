package home

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrivatePathAddsExistingToolchainBins(t *testing.T) {
	scope, owner, env := fixture(t)
	cargo := filepath.Join(owner, ".cargo/bin")
	goBin := filepath.Join(owner, "go/bin")
	bun := filepath.Join(owner, ".bun/bin")
	write(t, filepath.Join(cargo, "rustc"), "", 0700)
	write(t, filepath.Join(goBin, "gofmt"), "", 0700)
	out, err := Prepare(scope, "", env)
	if err != nil {
		t.Fatal(err)
	}
	fakeBin := envMap(env)["PATH"]
	// The publication bin keeps the first place. Owner entries keep their order,
	// and only the toolchain bins that exist are appended, in a fixed order.
	want := []string{filepath.Dir(out["DYNA_BIN"]), fakeBin, cargo, goBin}
	if got := strings.Split(out["PATH"], string(os.PathListSeparator)); !reflect.DeepEqual(got, want) {
		t.Fatalf("PATH: got %q want %q", got, want)
	}
	if strings.Contains(out["PATH"], bun) {
		t.Fatal("missing bun bin added to PATH")
	}
}

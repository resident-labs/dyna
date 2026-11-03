package images

import (
	"os"
	"strings"
	"testing"
)

// The repository image installs the toolchain on top of a non-root base image.
func TestDynaToolchainImageBuildUser(t *testing.T) {
	base, err := os.ReadFile("../../box/images/dyna-base/Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := os.ReadFile("../../../.dyna/images/dyna/Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	user := ""
	for _, line := range strings.Split(string(base)+"\n"+string(recipe), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "USER ") {
			user = strings.TrimSpace(strings.TrimPrefix(line, "USER "))
		}
		if strings.HasPrefix(line, "RUN ") && strings.Contains(line, "apt-get") && user != "" && user != "root" && user != "0" {
			t.Fatalf("toolchain install runs as %q, not root", user)
		}
	}
	if user != "node" {
		t.Fatalf("box must run as node, got %q", user)
	}
}

// The room image made from the repository's own folder passes the store's check, so `dyna image save` takes it.
func TestDynaToolchainImagePassesTheStoreCheck(t *testing.T) {
	recipe, err := os.ReadFile("../../../.dyna/images/dyna/Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkRecipe(string(recipe), "DYNA_BASE"); err != nil {
		t.Fatal(err)
	}
}

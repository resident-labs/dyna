package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The dyna binary re-execs itself as helper processes (processguard,
// sessionhost). Every dependency's package init runs in each helper before
// its private entrypoint, so chroma's lexer and style registries (about
// 4 MB retained per process) must stay out of the import graph. `dyna guide`
// keeps that cost out by printing raw markdown instead of using glamour.
func TestHelperImportGraphExcludesChroma(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "github.com/alecthomas/chroma") {
			t.Errorf("dyna binary depends on %s; every helper process pays its init", dep)
		}
	}
}

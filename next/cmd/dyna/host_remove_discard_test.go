package main

import (
	"strings"
	"testing"
)

func TestHostRemoveDiscardBoxesFlag(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "host", "remove", "pve")
	if f.hostRemove.Host != "pve" || f.hostRemove.DiscardBoxes {
		t.Fatalf("%+v", f.hostRemove)
	}
	runCLI(t, f, "host", "remove", "pve", "--discard-boxes")
	if f.hostRemove.Host != "pve" || !f.hostRemove.DiscardBoxes {
		t.Fatalf("%+v", f.hostRemove)
	}
	help := runCLI(t, f, "host", "remove", "--help")
	for _, want := range []string{"--discard-boxes", "not pushed is lost", "session restart", "whether or not the host answers"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help lacks %q:\n%s", want, help)
		}
	}
}

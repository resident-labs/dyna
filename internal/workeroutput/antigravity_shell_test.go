package workeroutput

import (
	"os"
	"strings"
	"testing"
)

func TestAntigravityMCPShell(t *testing.T) {
	// Captured from agy 1.4.2 session ses_20261020-014826-f8c35fee.
	raw, err := os.ReadFile("testdata/antigravity-shell.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("fixture has %d lines", len(lines))
	}
	decl := &Declaration{Dialect: DialectAntigravityStreamJSON}
	active := DecodeLine(decl, lines[0])
	if len(active.Events) != 0 {
		t.Fatalf("active tool was rendered twice: %+v", active.Events)
	}
	done := DecodeLine(decl, lines[1])
	if len(done.Events) != 1 {
		t.Fatalf("completion = %+v", done.Events)
	}
	e := done.Events[0]
	if e.Name != "dyna__shell" || e.State != "completed" || e.Input != `{"command":"echo short-ok","cwd":"/tmp/dyna/ses_20261019-235945-a4d72c47/live","description":"Run echo short-ok"}` || e.Output != "Exit code 0.\nshort-ok\n" {
		t.Fatalf("shell = %+v", e)
	}
}

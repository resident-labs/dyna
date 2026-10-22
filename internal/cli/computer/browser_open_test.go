package computer

import (
	"context"
	"dyna-agent/internal/computers"
	"fmt"
	"strings"
	"testing"
)

func TestBrowserOpenRequiresStatusBeforeComputerSetup(t *testing.T) {
	for _, intent := range []string{"", "  ", "Checking\nthe page", strings.Repeat("x", 81)} {
		command := NewBrowserOpenCommand()
		command.SilenceErrors, command.SilenceUsage = true, true
		command.SetArgs([]string{"https://example.test", "--status", intent})
		err := command.Execute()
		if err == nil || strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("status %q: %v", intent, err)
		}
	}
}

func TestBrowserOpenRoutesURLAndTrimmedStatusToActingComputer(t *testing.T) {
	var actor string
	call := browserFixture(t, func(_ context.Context, r computers.Request) (any, error) {
		if r.Op == "register" {
			actor = r.Session
			return computers.Meta{SessionID: r.Session, Room: r.Room}, nil
		}
		if r.Op != "browser.navigate" || r.Session != actor || r.Status != "Checking the example page" || len(r.BrowserArgs) != 1 || r.BrowserArgs[0] != "https://example.test/path" {
			return nil, fmt.Errorf("unexpected browser request: %+v", r)
		}
		return map[string]bool{"opened": true}, nil
	})
	out, err := call("open", "https://example.test/path", "--status", "  Checking the example page  ")
	if err != nil || !strings.Contains(out, `"opened":true`) {
		t.Fatalf("browser open: %q, %v", out, err)
	}
}

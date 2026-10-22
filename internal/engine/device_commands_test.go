package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/connect"
	"dyna-agent/internal/devicesvc"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc/devicestore"
)

func TestPrintDevicesUsesBothStores(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	web, err := devicestore.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	code, err := web.NewCode("")
	if err != nil {
		t.Fatal(err)
	}
	_, device, err := web.Pair(code, "paired phone")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := connectStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.UpdateSessions(func([]connect.Session) []connect.Session {
		return []connect.Session{{ID: "cs_phone", Name: "relay phone", End: time.Now().Add(time.Hour)}}
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := printDevices(devicesvc.New(subagent.DefaultStore().Root), &out); code != 0 {
		t.Fatalf("printDevices = %d", code)
	}
	for _, want := range []string{device.ID, "paired", "paired phone", "cs_phone", "connect", "relay phone", "legacy: full access"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q: %s", want, out.String())
		}
	}
	if err := os.WriteFile(filepath.Join(cs.Dir(), "sessions.json"), []byte("invalid JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := printDevices(devicesvc.New(subagent.DefaultStore().Root), &out); code != 1 || out.Len() != 0 {
		t.Fatalf("corrupt session store: code = %d, output = %q", code, out.String())
	}
}

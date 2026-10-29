package approvals

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	core "dyna-agent/internal/approvals"
)

type fakeClient struct {
	settings core.Settings
	calls    []core.SettingsInput
}

func (f *fakeClient) Call(_ context.Context, method string, args []any, out any) error {
	if method != "ApprovalSettings" {
		panic(method)
	}
	in := args[0].(core.SettingsInput)
	f.calls = append(f.calls, in)
	if in.Set {
		f.settings.Wait = in.Wait
		f.settings.Expiry = in.Expiry
	}
	*out.(*core.Settings) = f.settings
	return nil
}

func TestSettingsShowsAndUpdatesOneValue(t *testing.T) {
	client := &fakeClient{settings: core.Settings{Wait: core.DefaultWait, Expiry: core.DefaultExpiry}}
	run := func(args ...string) string {
		t.Helper()
		cmd := Command(Options{Client: client})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if out := run("settings"); !strings.Contains(out, "wait 1m0s") || len(client.calls) != 1 {
		t.Fatalf("show = %q, calls = %+v", out, client.calls)
	}
	out := run("--room", "alpha", "settings", "--wait", "90s")
	if !strings.Contains(out, "wait 1m30s") || client.settings.Expiry != 24*time.Hour {
		t.Fatalf("set = %q, settings = %+v", out, client.settings)
	}
	last := client.calls[len(client.calls)-1]
	if !last.Set || last.Room != "alpha" || last.Wait != 90*time.Second || last.Expiry != core.DefaultExpiry {
		t.Fatalf("update = %+v", last)
	}
}

package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// rename and adopt ask the engine, which checks the caller; the CLI process
// changes no session row itself.
func TestRenameAndAdoptCallTheEngine(t *testing.T) {
	storetest.Isolate(t)
	m := subagent.NewManager()
	work := workRoom(t, m)
	lead := subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", Name: "lead", Room: work.Name, RoomID: work.ID, Dir: t.TempDir(), Depth: 1, Created: time.Now()}
	other := lead
	other.ID, other.Name = "ses_20261017-000002-bbbbbbbb", "other"
	for _, meta := range []subagent.Meta{lead, other} {
		recordtest.Write(t, m.Store.Root, meta.ID, meta)
	}
	got := serveRecordedCalls(t, "RenameSession", "AdoptSession")
	runCLI(t, m, "rename", other.ID, "renamed")
	runCLI(t, m, "adopt", other.ID, "--under", lead.ID)
	wantCalls(t, got, map[string][]string{"RenameSession": {work.ID, other.ID, "renamed"}, "AdoptSession": {work.ID, other.ID, lead.ID}})
	if meta, err := m.Store.ReadMeta(other.ID); err != nil || meta.Name != "other" || meta.ParentSession != "" {
		t.Fatalf("the CLI changed the session itself: %+v, %v", meta, err)
	}
}

// serveRecordedCalls serves the named calls from a fake engine that only
// records their string arguments, so a test sees what the CLI asked for.
func serveRecordedCalls(t *testing.T, names ...string) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	calls := map[string]websvc.Call{}
	for _, name := range names {
		calls[name] = func(_ context.Context, args []json.RawMessage) (any, error) {
			for _, raw := range args {
				var s string
				if err := json.Unmarshal(raw, &s); err != nil {
					return nil, err
				}
				got[name] = append(got[name], s)
			}
			return nil, nil
		}
	}
	enginetest.Serve(t, store.DataDir(), calls)
	return got
}

func runCLI(t *testing.T, m *subagent.Manager, args ...string) {
	t.Helper()
	cmd := newCommand(func() *subagent.Manager { return m })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v: %v: %s", args, err, out.String())
	}
}

func wantCalls(t *testing.T, got, want map[string][]string) {
	t.Helper()
	for name, args := range want {
		if !slices.Equal(got[name], args) {
			t.Errorf("%s args = %q, want %q", name, got[name], args)
		}
	}
}

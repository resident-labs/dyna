package subagent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestShowPRLinksAndForceClose(t *testing.T) {
	storetest.Isolate(t)
	m := subagent.NewManager()
	work := workRoom(t, m)
	meta := subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", Name: "author", Room: work.Name, RoomID: work.ID, Dir: t.TempDir(), Created: time.Now()}
	recordtest.Write(t, m.Store.Root, meta.ID, meta)
	const key = "github.com/owner/repo#17"
	st := watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, Number: 17, URL: "https://github.com/owner/repo/pull/17", RoomID: meta.RoomID, Author: meta.ID, State: "open", Watching: true}}}
	root := watch.DefaultStore().Root
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(root, "state.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	serveSessionControls(t, m)
	call := func(args ...string) (string, error) {
		cmd := newCommand(func() *subagent.Manager { return m })
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	if out, err := call("show", meta.ID); err != nil || !strings.Contains(out, "PR #17 https://github.com/owner/repo/pull/17 · open · author") {
		t.Fatalf("plain PR links: %s %v", out, err)
	}
	if out, err := call("show", meta.ID, "--json"); err != nil || !strings.Contains(out, `"trackedPRs"`) || !strings.Contains(out, `"roles"`) {
		t.Fatalf("JSON PR links: %s %v", out, err)
	}
	if _, err := call("close", meta.ID); err == nil || !strings.HasPrefix(err.Error(), "responsibilities:") {
		t.Fatalf("CLI close guard: %v", err)
	}
	if out, err := call("close", meta.ID, "--force"); err != nil || !strings.Contains(out, "closed work:author") {
		t.Fatalf("CLI force close: %s %v", out, err)
	}
}

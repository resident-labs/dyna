package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/service"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/engineclient"
)

func TestUsageListsCachedAccounts(t *testing.T) {
	now := time.Now().UTC()
	store := &accountstore.Store{Root: filepath.Join(t.TempDir(), "accounts")}
	used := 41.0
	if err := store.Update(func(s *accountstore.State) error {
		for _, h := range []string{"codex", "muse"} {
			id := strings.Repeat(h[:1], 32)
			s.Entries = append(s.Entries, accountstore.Entry{Metadata: accountstore.Metadata{ID: id, Harness: h, Label: h + "-label", NativeHome: filepath.Join(store.Root, h, id),
				Observation: &accountstore.Observation{ObservedAt: now, Windows: []accountstore.Window{{ID: "w", Label: "weekly", UsedPercent: &used}}}}})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		cmd := Command(Options{Store: store})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err, out.String())
		}
		return out.String()
	}
	text := run()
	if !strings.Contains(text, "codex  codex-label") || !strings.Contains(text, "weekly 41%") || !strings.Contains(text, "muse  muse-label") {
		t.Fatalf("text output = %q", text)
	}
	only := run("--harness", "muse")
	if strings.Contains(only, "codex") || !strings.Contains(only, "muse") {
		t.Fatalf("--harness muse output = %q", only)
	}
	var snap service.Snapshot
	if err := json.Unmarshal([]byte(run("--json", "--harness", "codex")), &snap); err != nil || len(snap.Accounts) != 1 || snap.Accounts[0].Harness != "codex" {
		t.Fatalf("json = %+v, %v", snap, err)
	}
}

func TestUsageReportsStoreReadFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	run := func() error {
		cmd := Command(Options{Store: &accountstore.Store{Root: root}})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(nil)
		return cmd.Execute()
	}
	if err := run(); err != nil {
		t.Fatalf("empty store: %v", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "accounts.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil {
		t.Fatal("malformed store: want an error")
	}
}

func TestRefreshRequiresEngineButCachedReadsDoNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sock")
	for _, refresh := range []bool{false, true} {
		cmd := Command(Options{Store: &accountstore.Store{Root: t.TempDir()}, Client: engineclient.New(path, engineclient.RestartWait(0))})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if refresh {
			cmd.SetArgs([]string{"--refresh"})
		} else {
			cmd.SetArgs(nil)
		}
		err := cmd.Execute()
		if refresh && !errors.Is(err, engineclient.ErrNotRunning) {
			t.Fatalf("refresh: %v", err)
		}
		if !refresh && err != nil {
			t.Fatalf("cached read: %v", err)
		}
	}
}

func TestMuseUsageShowsBothResetsAndObservationAge(t *testing.T) {
	store := &accountstore.Store{Root: t.TempDir()}
	now := time.Now().UTC()
	used := 12.0
	reset := now.Add(time.Hour)
	weekly := now.Add(5 * 24 * time.Hour)
	err := store.Update(func(s *accountstore.State) error {
		s.Entries = append(s.Entries, accountstore.Entry{Metadata: accountstore.Metadata{ID: strings.Repeat("1", 32), Harness: "muse", Label: "Muse", NativeHome: filepath.Join(store.Root, "muse", "fixture"), Observation: &accountstore.Observation{ObservedAt: now.Add(-3 * time.Minute), Windows: []accountstore.Window{{ID: "subscription", Label: "Session (5-hour) (idle)", UsedPercent: &used, ResetsAt: reset.Format(time.RFC3339)}, {ID: "weekly", Label: "Weekly", UsedPercent: &used, ResetsAt: weekly.Format(time.RFC3339)}}}}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := Command(Options{Store: store})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--harness", "muse"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"idle", "12%", "observed 3m", reset.Local().Format("Mon 15:04"), weekly.Local().Format("Mon 15:04")} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}

func TestUsageShowsProbablyResetAndBlockedUntil(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	observed := now.Add(-time.Hour)
	passed, future := now.Add(-time.Minute), now.Add(time.Hour)
	st := &accountstore.Store{Root: t.TempDir()}
	used := 100.0
	if err := st.Update(func(state *accountstore.State) error {
		for _, row := range []struct {
			id, label string
			reset     time.Time
		}{{strings.Repeat("1", 32), "Reset account", passed}, {strings.Repeat("2", 32), "Blocked account", future}} {
			state.Entries = append(state.Entries, accountstore.Entry{Metadata: accountstore.Metadata{ID: row.id, Harness: "codex", Label: row.label, NativeHome: filepath.Join(st.Root, "codex", row.id), Observation: &accountstore.Observation{ObservedAt: observed, Windows: []accountstore.Window{{ID: "seven_day", Label: "Weekly", UsedPercent: &used, ResetsAt: row.reset.Format(time.RFC3339)}}}}})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cmd := Command(Options{Store: st})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Weekly: probably reset · last checked "+observed.Local().Format("Mon 3:04 PM")) {
		t.Fatal(text)
	}
	if !strings.Contains(text, "blocked until "+future.Local().Format("Mon 3:04 PM")) {
		t.Fatal(text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "Reset account") && strings.Contains(line, "%") {
			t.Fatal("CLI showed a guess as a number", line)
		}
	}
}

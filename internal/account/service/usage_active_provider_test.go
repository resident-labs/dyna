package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	accountstore "dyna-agent/internal/account/store"
)

func TestCachedUsageMarksAKeyedOpenCodeAccountActive(t *testing.T) {
	st := &accountstore.Store{Root: filepath.Join(t.TempDir(), "accounts")}
	id := strings.Repeat("c", 32)
	if err := os.MkdirAll(filepath.Join(st.Root, "opencode", id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(state *accountstore.State) error {
		state.Entries = append(state.Entries, accountstore.Entry{Metadata: accountstore.Metadata{
			ID: id, Harness: "opencode", Provider: accountstore.ProviderZAI, Kind: accountstore.KindAPIKey,
			Identity: "opencode:zai:fixture", NativeHome: filepath.Join(st.Root, "opencode", id), Label: "zai key",
		}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActive(accountstore.ProviderOpenCodeGo, "opencode", "", id); err != nil {
		t.Fatal(err)
	}
	rows := CachedUsage(st, time.Now().UTC()).Accounts
	if len(rows) != 1 || !rows[0].Active || rows[0].Provider != accountstore.ProviderZAI {
		t.Fatalf("keyed OpenCode account = %+v, want active with its zai provider", rows)
	}
}

func TestCachedUsageMarksAPiAccountActiveByItsProvider(t *testing.T) {
	st := &accountstore.Store{Root: filepath.Join(t.TempDir(), "accounts")}
	zai, deepseek := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, id := range []string{zai, deepseek} {
		if err := os.MkdirAll(filepath.Join(st.Root, "pi", id), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Update(func(state *accountstore.State) error {
		state.Entries = append(state.Entries,
			accountstore.Entry{Metadata: accountstore.Metadata{ID: zai, Harness: "pi", Provider: accountstore.ProviderZAI, Kind: accountstore.KindAPIKey, Identity: "pi:zai", NativeHome: filepath.Join(st.Root, "pi", zai), Label: "zai"}},
			accountstore.Entry{Metadata: accountstore.Metadata{ID: deepseek, Harness: "pi", Provider: accountstore.ProviderDeepSeek, Kind: accountstore.KindAPIKey, Identity: "pi:deepseek", NativeHome: filepath.Join(st.Root, "pi", deepseek), Label: "deepseek"}},
		)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActive(accountstore.ProviderZAI, "pi", "", zai); err != nil {
		t.Fatal(err)
	}
	active := map[string]bool{}
	for _, row := range CachedUsage(st, time.Now().UTC()).Accounts {
		active[row.Label] = row.Active
	}
	if !active["zai"] || active["deepseek"] {
		t.Fatalf("Pi active marks = %v, want only zai active", active)
	}
}

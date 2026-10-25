package subagent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workeroutput"
)

func TestShowUnavailableTokenUsage(t *testing.T) {
	unknown := workeroutput.UsageNormalization{Version: 1, Source: "harness-reported", Input: "unknown", CachedInput: "unknown", Output: "unknown", Reason: "JSON output does not report provider usage"}
	measured := workeroutput.UsageNormalization{Version: 1, Source: "harness-reported", Input: "complete", CachedInput: "complete", Output: "complete"}
	for _, tc := range []struct {
		name           string
		normalizations []workeroutput.UsageNormalization
		usage          workeroutput.Usage
		want           string
		unfinished     string
	}{
		{"unavailable", []workeroutput.UsageNormalization{unknown}, workeroutput.Usage{}, "usage unavailable", ""},
		{"measured zero", []workeroutput.UsageNormalization{measured}, workeroutput.Usage{}, "tokens in 0 (cached 0) out 0", ""},
		{"earlier measured zero", []workeroutput.UsageNormalization{measured, unknown}, workeroutput.Usage{}, "tokens in 0 (cached 0) out 0", ""},
		{"no turns", nil, workeroutput.Usage{}, "tokens in 0 (cached 0) out 0", ""},
		{"legacy", []workeroutput.UsageNormalization{{}}, workeroutput.Usage{}, "tokens in 0 (cached 0) out 0", ""},
		{"earlier measured turn", []workeroutput.UsageNormalization{measured, unknown}, workeroutput.Usage{InputTokens: 12}, "tokens in 12 (cached 0) out 0", ""},
		{"unknown then queued", []workeroutput.UsageNormalization{unknown, {}}, workeroutput.Usage{}, "usage unavailable", subagent.TurnQueued},
		{"unknown then running", []workeroutput.UsageNormalization{unknown, {}}, workeroutput.Usage{}, "usage unavailable", subagent.TurnRunning},
		{"only queued", []workeroutput.UsageNormalization{{}}, workeroutput.Usage{}, "tokens in 0 (cached 0) out 0", subagent.TurnQueued},
		{"only running", []workeroutput.UsageNormalization{{}}, workeroutput.Usage{}, "tokens in 0 (cached 0) out 0", subagent.TurnRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			m := subagent.NewManager()
			room := workRoom(t, m)
			meta := subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", Name: "usage", Room: room.Name, RoomID: room.ID, Dir: t.TempDir(), Created: time.Now(), Turns: len(tc.normalizations), Usage: tc.usage}
			dir, err := m.Store.Dir(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
				t.Fatal(err)
			}
			write := func(path string, value any) {
				t.Helper()
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			recordtest.Write(t, m.Store.Root, meta.ID, meta)
			if tc.unfinished != "" {
				lock, err := os.OpenFile(filepath.Join(dir, "turn.lock"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { lock.Close() })
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			}
			for i, normalization := range tc.normalizations {
				turn := subagent.Turn{N: i + 1, Status: subagent.TurnOK, OutputDialect: workeroutput.DialectPiJSON, UsageNormalization: normalization}
				if i == len(tc.normalizations)-1 && tc.unfinished != "" {
					turn.Status = tc.unfinished
				}
				write(filepath.Join(dir, "turns", strconv.Itoa(i+1)+".json"), turn)
			}
			cmd := newCommand(func() *subagent.Manager { return m })
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"show", meta.ID, "--turns", "1"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("show = %q, want %q", out.String(), tc.want)
			}
		})
	}
}

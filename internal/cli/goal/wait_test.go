package goal

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/goalkeeper"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestParseUntil(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		value   string
		want    int64
		invalid bool
	}{
		{"", 0, true},
		{"30m", now.Add(30 * time.Minute).UnixMilli(), false},
		{"2026-10-18T14:00:00+01:00", now.Add(time.Hour).UnixMilli(), false},
		{"2026-10-18T13:00:00.123Z", now.Add(time.Hour + 123*time.Millisecond).UnixMilli(), false},
		{"0s", 0, true}, {"-1s", 0, true}, {"1ns", 0, true}, {"tomorrow", 0, true},
		{"2026-10-18T11:00:00Z", 0, true}, {"2026-10-18T12:00:00Z", 0, true},
	} {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseUntil(tt.value, now)
			if (err != nil) != tt.invalid || got != tt.want {
				t.Fatalf("parseUntil(%q) = %d, %v; want %d, invalid %v", tt.value, got, err, tt.want, tt.invalid)
			}
		})
	}
}

func TestWaitCommandCallerAndDeadline(t *testing.T) {
	for _, session := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "session"}[session], func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			root := t.TempDir()
			rooms := &room.Store{Root: filepath.Join(root, "rooms")}
			rec, err := rooms.Create(room.CreateInput{Name: "work"})
			if err != nil {
				t.Fatal(err)
			}
			mgr := subagent.NewManager()
			mgr.Store = &subagent.Store{Root: filepath.Join(root, "sessions")}
			mgr.Rooms = rooms
			s := goalkeeper.New()
			s.Store = &goals.Store{Root: filepath.Join(root, "goals")}
			s.Notify = notify.New(rooms, mgr)
			actor := authority.Actor{Role: authority.Owner}
			serveMutations(t, s, &actor)
			by := "owner"
			if session {
				by = "ses_20261017-000001-aaaaaaaa"
				actor = authority.Actor{Role: authority.Session, Session: by}
				meta := subagent.Meta{ID: by, Name: "worker", RoomID: rec.ID, Room: rec.Name, Depth: 1, Resumable: true}
				recordtest.Write(t, mgr.Store.Root, by, meta)
				t.Setenv(mode.EnvMode, mode.Subagent)
				t.Setenv(mode.EnvSession, by)
				t.Setenv(mode.EnvRoomID, rec.ID)
				t.Setenv(mode.EnvRoom, rec.Name)
			}
			g, err := s.Store.Add(rec.ID, "owner", goals.Input{Text: "Ship the relay", Shared: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "30m"} {
				cmd := Command(Options{Service: func() *goalkeeper.Service { return s }})
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				args := []string{"wait", g.ID, "--until", value, "--json"}
				if !session {
					args = append(args, "--room", rec.Name)
				}
				cmd.SetArgs(args)
				if err := cmd.Execute(); err != nil {
					t.Fatalf("%s: %v: %s", value, err, out.String())
				}
				var got goals.Goal
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				last := got.Edits[len(got.Edits)-1]
				if last.By != by || !got.UpdatedAt.Equal(g.UpdatedAt) || got.WaitUntil.IsZero() {
					t.Fatalf("wait result = %#v", got)
				}
			}
			messages, err := rooms.Messages(rec.ID)
			if err != nil || len(messages) != 0 {
				t.Fatalf("wait emitted messages: %#v, %v", messages, err)
			}

			for _, value := range []string{"", "0s", "bad"} {
				cmd := Command(Options{Service: func() *goalkeeper.Service { return s }})
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				cmd.SetArgs([]string{"wait", g.ID, "--until", value})
				if err := cmd.Execute(); err == nil {
					t.Fatalf("accepted %q", value)
				}
			}
		})
	}
}

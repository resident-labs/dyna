package subagent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/database"
	"dyna-agent/internal/turnlock"
)

func TestPromptCutoverRequiresQuiescentOldWriters(t *testing.T) {
	if callerid.StoreFormat != promptStoreFormat {
		t.Fatal("launch records must advertise the active prompt format")
	}
	unavailable := errors.New("source unavailable or malformed")
	for _, tc := range []struct {
		name   string
		edit   func(*promptCutoverProbes)
		reject bool
	}{
		{"old turn", func(p *promptCutoverProbes) {
			p.launches = func() ([]promptLaunch, error) {
				return []promptLaunch{{callerid.Process{PID: 7, Start: 1}, 2, "turn"}}, nil
			}
		}, true},
		{"old consolidate", func(p *promptCutoverProbes) {
			p.launches = func() ([]promptLaunch, error) {
				return []promptLaunch{{callerid.Process{PID: 7, Start: 1}, 2, "consolidate"}}, nil
			}
		}, true},
		{"old transfer", func(p *promptCutoverProbes) {
			p.launches = func() ([]promptLaunch, error) {
				return []promptLaunch{{callerid.Process{PID: 7, Start: 1}, 2, "transfer"}}, nil
			}
		}, true},
		{"old resume room", func(p *promptCutoverProbes) {
			p.launches = func() ([]promptLaunch, error) {
				return []promptLaunch{{callerid.Process{PID: 7, Start: 1}, 2, "resume-room"}}, nil
			}
		}, true},
		{"unrelated launch", func(p *promptCutoverProbes) {
			p.launches = func() ([]promptLaunch, error) {
				return []promptLaunch{{callerid.Process{PID: 7, Start: 1}, 2, "shell"}}, nil
			}
		}, false},
		{"compatible launch", func(p *promptCutoverProbes) {
			p.launches = func() ([]promptLaunch, error) {
				return []promptLaunch{{callerid.Process{PID: 7, Start: 1}, 3, "turn"}}, nil
			}
		}, false},
		{"launch source error", func(p *promptCutoverProbes) { p.launches = func() ([]promptLaunch, error) { return nil, unavailable } }, true},
		{"candidate source error", func(p *promptCutoverProbes) {
			p.candidates = func() ([]promptRunner, error) { return nil, unavailable }
		}, true},
		{"unregistered runner", func(p *promptCutoverProbes) {
			p.launch = func(callerid.Process) (callerid.Launched, bool, error) { return callerid.Launched{}, false, nil }
		}, true},
		{"unknown runner", func(p *promptCutoverProbes) { p.matches = func(int, string, int) (bool, bool) { return false, false } }, true},
		{"unknown process", func(p *promptCutoverProbes) {
			p.process = func(int) (callerid.Process, error) { return callerid.Process{}, unavailable }
		}, true},
		{"dead process", func(p *promptCutoverProbes) {
			p.process = func(int) (callerid.Process, error) { return callerid.Process{}, os.ErrNotExist }
		}, false},
		{"unrelated runner", func(p *promptCutoverProbes) { p.matches = func(int, string, int) (bool, bool) { return false, true } }, false},
		{"launch lookup error", func(p *promptCutoverProbes) {
			p.launch = func(callerid.Process) (callerid.Launched, bool, error) {
				return callerid.Launched{}, false, unavailable
			}
		}, true},
		{"old runner", func(p *promptCutoverProbes) {
			p.launch = func(callerid.Process) (callerid.Launched, bool, error) {
				return callerid.Launched{Identity: callerid.Identity{Kind: callerid.Session, Session: "session"}, Format: 2}, true, nil
			}
		}, true},
		{"wrong owner", func(p *promptCutoverProbes) {
			p.launch = func(callerid.Process) (callerid.Launched, bool, error) {
				return callerid.Launched{Format: 3}, true, nil
			}
		}, true},
		{"reused launch pid", func(p *promptCutoverProbes) {
			p.launches = func() ([]promptLaunch, error) {
				return []promptLaunch{{callerid.Process{PID: 7, Start: 99}, 2, "turn"}}, nil
			}
		}, false},
		{"dead old launch", func(p *promptCutoverProbes) {
			p.launches = func() ([]promptLaunch, error) {
				return []promptLaunch{{callerid.Process{PID: 7, Start: 1}, 2, "turn"}}, nil
			}
			p.process = func(int) (callerid.Process, error) { return callerid.Process{}, os.ErrNotExist }
		}, false},
		{"unknown start", func(p *promptCutoverProbes) {
			p.process = func(pid int) (callerid.Process, error) { return callerid.Process{PID: pid}, nil }
		}, true},
		{"runner pid changed during probe", func(p *promptCutoverProbes) {
			start := uint64(0)
			p.process = func(pid int) (callerid.Process, error) { start++; return callerid.Process{PID: pid, Start: start}, nil }
		}, true},

		{"compatible runner", func(p *promptCutoverProbes) {}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := promptCutoverProbes{
				launches:   func() ([]promptLaunch, error) { return nil, nil },
				candidates: func() ([]promptRunner, error) { return []promptRunner{{"session", 7, 1}}, nil },
				process:    func(pid int) (callerid.Process, error) { return callerid.Process{PID: pid, Start: 1}, nil },
				matches:    func(int, string, int) (bool, bool) { return true, true },
				launch: func(callerid.Process) (callerid.Launched, bool, error) {
					return callerid.Launched{Identity: callerid.Identity{Kind: callerid.Session, Session: "session"}, Format: 3}, true, nil
				},
			}
			tc.edit(&p)
			if err := checkPromptCutover(p); (err != nil) != tc.reject {
				t.Fatalf("preflight error = %v; reject = %v", err, tc.reject)
			}
		})
	}
	t.Run("gate source requires an explicit pid", func(t *testing.T) {
		dataDir := t.TempDir()
		db, err := database.Open(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { database.Close(dataDir) })
		id := NewID()
		sessions := filepath.Join(dataDir, "sessions")
		dir := filepath.Join(sessions, id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		// Session metadata may omit PID when it has no runner.
		if _, err := db.Exec("CREATE TABLE session_records(id TEXT, meta TEXT); INSERT INTO session_records VALUES (?, '{}')", id); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			gate   string
			reject bool
		}{
			{`{"active":true,"turn":1}`, true},
			{`{"active":true,"turn":1,"pid":null}`, true},
			{`{"active":true,"turn":1,"pid":0}`, false},
		} {
			if err := os.WriteFile(filepath.Join(dir, turnlock.GateFile), []byte(tc.gate), 0600); err != nil {
				t.Fatal(err)
			}
			candidates, err := promptRunners(db, sessions)
			if (err != nil) != tc.reject {
				t.Fatalf("gate %s: error = %v; reject = %v", tc.gate, err, tc.reject)
			}
			if len(candidates) != 0 {
				t.Fatalf("unexpected candidates: %v", candidates)
			}
		}
	})

}

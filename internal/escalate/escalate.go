// Package escalate picks who acts when a PR or a goal needs a decision.
//
// The walk starts at the subject session and follows ParentSession up to the
// top. The first open session wins. When nobody in the chain is open, one new
// agent starts in the room, once per key. The owner is the last resort.
package escalate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

// Request describes one escalation.
type Request struct {
	Room    string   // room ID
	Subject string   // session ID or address where the walk starts; may be empty
	Skip    []string // session IDs the walk passes over
	Key     string   // dedupe key: at most one spawned agent per key
	Goal    string   // goal for a spawned agent: outcome and done condition
	Brief   string   // task for a spawned agent; empty means never start one
}

// Start launches a session. Callers pass Manager.Start; tests pass a fake.
type Start func(subagent.StartOptions) (subagent.Info, error)

// Open reports whether a message can wake the session. Every session that is
// not closed counts, including idle and dormant ones.
func Open(info subagent.Info) bool {
	return info.Status != subagent.StatusClosed
}

// Route returns the session to wake. A zero Info means the owner must act:
// nobody in the chain is open and no agent could start. spawned is true only
// when this call started the agent.
func Route(ctx context.Context, m *subagent.Manager, start Start, r Request) (info subagent.Info, spawned bool, err error) {
	profile := ""
	seen := map[string]bool{}
	for id := r.Subject; id != ""; {
		next, e := m.Store.DeliveryTarget(id)
		if e != nil || next.RoomID != r.Room || seen[next.ID] {
			break
		}
		seen[next.ID] = true
		if profile == "" {
			profile = next.Profile
		}
		if Open(next) && !slices.Contains(r.Skip, next.ID) {
			return next, false, nil
		}
		id = next.ParentSession
	}
	if r.Brief == "" {
		return subagent.Info{}, false, nil
	}
	return spawn(ctx, m, start, r, profile)
}

type record struct {
	Key     string    `json:"key"`
	Session string    `json:"session"`
	Profile string    `json:"profile"`
	At      time.Time `json:"at"`
}

func dataDir(m *subagent.Manager) string {
	root := m.Store.Root
	if root == "" {
		root = runstore.SessionsDir()
	}
	return filepath.Dir(root)
}

func dedupeHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// spawn starts one agent per key. A saved record means an agent already
// started for this key: route to it while it is open, else to the owner.
func spawn(ctx context.Context, m *subagent.Manager, start Start, r Request, profile string) (subagent.Info, bool, error) {
	dir := dataDir(m)
	path := filepath.Join(dir, "locks", "escalations", dedupeHash(r.Key)+".lock")
	unlock, err := lockKey(ctx, path)
	if err != nil {
		return subagent.Info{}, false, fmt.Errorf("escalation for %s is busy: %w", r.Key, err)
	}
	defer unlock()
	db, err := openRecords(dir)
	if err != nil {
		return subagent.Info{}, false, err
	}
	w := state.For(db)
	rec, found, err := readRecord(ctx, w, r.Key)
	if err != nil {
		return subagent.Info{}, false, err
	}
	if found {
		if info, err := m.Store.DeliveryTarget(rec.Session); err == nil && Open(info) {
			return info, false, nil
		}
		return subagent.Info{}, false, nil
	}
	if err := ctx.Err(); err != nil {
		return subagent.Info{}, false, err
	}
	// Manager.Start has no context or finite duration bound. Hold only this key's
	// file lock during spawn; other keys and the database writer remain free.
	info, err := startWithFallback(m, start, r, usable(m, profile))
	if err != nil {
		log.Printf("escalation %s: no agent started: %v", r.Key, err)
		return subagent.Info{}, false, nil
	}
	// An already-started session must keep its dedupe record after cancellation.
	completion, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	rec = record{Key: r.Key, Session: info.ID, Profile: info.Profile, At: time.Now()}
	if err := saveRecord(completion, w, rec); err != nil {
		return info, true, fmt.Errorf("escalation %s started %s but could not save its dedupe record: %w", r.Key, info.ID, err)
	}
	return info, true, nil
}

// startWithFallback tries the subject's profile, then the default profile.
func startWithFallback(m *subagent.Manager, start Start, r Request, profile string) (subagent.Info, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return subagent.Info{}, fmt.Errorf("get escalation working directory: %w", err)
	}
	opts := subagent.StartOptions{Room: r.Room, Profile: profile, Goal: r.Goal, Task: r.Brief, Cwd: cwd}
	info, err := start(opts)
	if err != nil && profile != "" {
		opts.Profile = ""
		info, err = start(opts)
	}
	return info, err
}

// usable returns profile when it is enabled and not cooling down after a
// provider failure, such as a quota limit. Otherwise "" picks the default.
func usable(m *subagent.Manager, profile string) string {
	if profile == "" {
		return ""
	}
	ps, err := m.ProfileStore()
	if err != nil {
		return ""
	}
	failures, err := rolesession.RecentProfileFailures(m.Store, time.Now())
	if err != nil {
		return ""
	}
	for _, p := range rolesession.RankProfiles(ps.Profiles, failures, time.Now(), "") {
		if p.Name == profile && p.ForSubagents() {
			return profile
		}
	}
	return ""
}

func lockKey(ctx context.Context, path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	return store.LockContext(ctx, path)
}

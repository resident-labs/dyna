package connect

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/fsutil"
)

// Store keeps the machine's Connect state in <data dir>/connect, mode 0700:
//
//	machine.key    the Ed25519 seed of the machine key (0600)
//	link.json      the link: relay, machine ID, owner, host names, epoch
//	tunnel-token   the cloudflared connector token (0600); never logged
//	jwks.json      the relay's public keys and when they were fetched
//	sessions.json  Connect sessions: token hashes only, never a token
//	audit.log      one JSON line per Connect event on this machine
//	push/          Web Push: the VAPID key and one subscription per device
//
// The server and the connect commands are separate processes. Each change
// takes an exclusive flock on connect/.lock and replaces the file by
// rename. The server reads sessions.json again when it changes, so a revoke
// from the command line takes effect on the next request.
type Store struct {
	dir string
	now func() time.Time

	mu       sync.Mutex
	stamp    string
	sessions []Session
}

// Link is this machine's link to a relay account.
type Link struct {
	Relay   string    `json:"relay"`
	Machine string    `json:"machine"`
	Name    string    `json:"name"`
	Owner   Owner     `json:"owner"`
	App     string    `json:"app"`
	Cards   string    `json:"cards"`
	Epoch   int       `json:"epoch"`
	Linked  time.Time `json:"linked"`
	// Unlinked is set when the relay says the link is gone. The machine
	// stops its tunnel and refuses Connect sessions.
	Unlinked bool `json:"unlinked,omitempty"`
}

// Owner is the relay account that owns this machine.
type Owner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Session is one Connect session: one browser key on one route origin,
// under one relay device of the owner.
type Session struct {
	ID       string `json:"id"`
	Hash     string `json:"hash"`
	User     string `json:"user"`
	UserName string `json:"userName"`
	Device   string `json:"device"`
	JKT      string `json:"jkt"`
	Origin   string `json:"origin"`
	Epoch    int    `json:"epoch"`
	Name     string `json:"name"`
	// Old holds the hashes of the last 3 cookie values, newest first. The
	// newest works in full until PrevUntil (requests in flight during a
	// renew); after that, and for the older ones, a request can only renew,
	// which needs the session key.
	Old       []string  `json:"old,omitempty"`
	PrevUntil time.Time `json:"prevUntil,omitempty"`
	Created   time.Time `json:"created"`
	LastSeen  time.Time `json:"lastSeen"`
	// Access is the end of the short access window; renew moves it.
	Access time.Time `json:"access"`
	// End is the grant's absolute end. Renew never passes it.
	End time.Time `json:"end"`
	// Grant is the last time the relay said the device may use this machine.
	Grant time.Time `json:"grant"`
}

// OpenStore opens (and makes) the state directory.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, now: time.Now}, nil
}

// Dir is the state directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

func (s *Store) locked(fn func() error) error {
	f, err := os.OpenFile(s.path(".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

func (s *Store) write(name string, b []byte) error {
	return fsutil.WriteFile(s.path(name), b, 0o600, false)
}

func (s *Store) readJSON(name string, v any) (bool, error) {
	b, err := os.ReadFile(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("%s: %w", s.path(name), err)
	}
	return true, nil
}

func (s *Store) writeJSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return s.write(name, b)
}

// --- machine key ---

// Key loads the machine key, making it on first use.
func (s *Store) Key() (ed25519.PrivateKey, error) {
	var key ed25519.PrivateKey
	err := s.locked(func() error {
		b, err := os.ReadFile(s.path("machine.key"))
		if err == nil {
			seed, derr := b64.DecodeString(string(b))
			if derr != nil || len(seed) != ed25519.SeedSize {
				return fmt.Errorf("%s is damaged", s.path("machine.key"))
			}
			key = ed25519.NewKeyFromSeed(seed)
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return err
		}
		key = ed25519.NewKeyFromSeed(seed)
		return s.write("machine.key", []byte(b64.EncodeToString(seed)))
	})
	return key, err
}

// ResetKey makes a new machine key. Unlink calls it, so a later link is a
// new machine to the relay.
func (s *Store) ResetKey() error {
	return s.locked(func() error {
		err := os.Remove(s.path("machine.key"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
}

// --- link ---

// Link reads link.json. ok is false when this machine was never linked.
func (s *Store) Link() (Link, bool, error) {
	var l Link
	ok, err := s.readJSON("link.json", &l)
	return l, ok && l.Machine != "", err
}

// SaveLink writes link.json.
func (s *Store) SaveLink(l Link) error {
	return s.locked(func() error { return s.writeJSON("link.json", l) })
}

// ClearLink removes the link, the tunnel token, the key set and every
// Connect session.
func (s *Store) ClearLink() error {
	return s.locked(func() error {
		for _, n := range []string{"link.json", "tunnel-token", "jwks.json", "sessions.json"} {
			if err := os.Remove(s.path(n)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	})
}

// TunnelTokenPath is the file cloudflared reads with --token-file.
func (s *Store) TunnelTokenPath() string { return s.path("tunnel-token") }

// SaveTunnelToken writes the connector token, mode 0600.
func (s *Store) SaveTunnelToken(tok string) error {
	return s.locked(func() error { return s.write("tunnel-token", []byte(tok)) })
}

// --- relay keys ---

// KeySet reads the cached relay key set.
func (s *Store) KeySet() (KeySet, error) {
	var ks KeySet
	_, err := s.readJSON("jwks.json", &ks)
	return ks, err
}

// SaveKeySet caches the relay key set.
func (s *Store) SaveKeySet(ks KeySet) error {
	return s.locked(func() error { return s.writeJSON("jwks.json", ks) })
}

// --- audit ---

// Audit appends one line to audit.log. Lines name sessions and devices by
// ID only; they never hold a token.
func (s *Store) Audit(action, detail string) {
	line, _ := json.Marshal(map[string]string{"at": s.now().UTC().Format(time.RFC3339), "action": action, "detail": detail})
	f, err := os.OpenFile(s.path("audit.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// --- sessions ---

func (s *Store) stampOf() string {
	st, err := os.Stat(s.path("sessions.json"))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", st.Size(), st.ModTime().UnixNano())
}

func (s *Store) loadSessions() ([]Session, error) {
	var list []Session
	_, err := s.readJSON("sessions.json", &list)
	return list, err
}

// Sessions lists the live Connect sessions (not past their absolute end).
func (s *Store) Sessions() ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refresh(); err != nil {
		return nil, err
	}
	now := s.now()
	var out []Session
	for _, x := range s.sessions {
		if now.Before(x.End) {
			out = append(out, x)
		}
	}
	return out, nil
}

// refresh reloads sessions.json when it changed. The caller holds mu.
func (s *Store) refresh() error {
	st := s.stampOf()
	if st == s.stamp && st != "" {
		return nil
	}
	list, err := s.loadSessions()
	if err != nil {
		return err
	}
	s.sessions, s.stamp = list, st
	return nil
}

// UpdateSessions changes the session list under the lock. fn gets the
// current list and returns the new one.
func (s *Store) UpdateSessions(fn func([]Session) []Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		list, err := s.loadSessions()
		if err != nil {
			return err
		}
		now := s.now()
		next := fn(list)
		kept := next[:0]
		for _, x := range next {
			if now.Before(x.End) {
				kept = append(kept, x)
			}
		}
		if err := s.writeJSON("sessions.json", kept); err != nil {
			return err
		}
		s.sessions, s.stamp = kept, s.stampOf()
		return nil
	})
}

// RevokeSessions ends sessions by session ID, by relay device ID, or all
// of them ("all"). It returns how many ended.
func (s *Store) RevokeSessions(id string) (int, error) {
	n := 0
	err := s.UpdateSessions(func(list []Session) []Session {
		var out []Session
		for _, x := range list {
			if id == "all" || x.ID == id || x.Device == id {
				n++
				continue
			}
			out = append(out, x)
		}
		return out
	})
	return n, err
}

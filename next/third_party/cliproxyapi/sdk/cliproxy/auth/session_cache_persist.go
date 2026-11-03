package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	log "github.com/sirupsen/logrus"
)

// Dyna: session bindings survive a restart, so a session keeps the account that holds
// its prompt cache. The file holds each live binding with its absolute expiry, so the
// TTL is unchanged; failover and invalidation work as before because a loaded entry is
// an ordinary cache entry.

const sessionCacheFlushInterval = time.Second

type persistedSession struct {
	AuthID    string    `json:"auth_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Aliases   []string  `json:"aliases"`
	SessionID string    `json:"session_id,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	Sequence  uint64    `json:"sequence,omitempty"`
}

// NewPersistentSessionCache is NewSessionCache backed by the file at path: it loads
// the unexpired bindings now and rewrites the file shortly after any change.
func NewPersistentSessionCache(ttl time.Duration, path string) *SessionCache {
	c := NewSessionCache(ttl)
	c.statePath = path
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warnf("session affinity state not loaded: %v", err)
		}
	} else {
		var saved []persistedSession
		if err := json.Unmarshal(data, &saved); err != nil {
			log.Warnf("session affinity state not loaded: %v", err)
		}
		c.mu.Lock()
		c.ensureInitializedLocked()
		now := time.Now()
		for _, s := range saved {
			if s.AuthID == "" || len(s.Aliases) == 0 || !now.Before(s.ExpiresAt) {
				continue
			}
			if s.Sequence > c.bindingSequence {
				c.bindingSequence = s.Sequence
			}
			c.replaceAliasGroupsLocked(s.AuthID, s.ExpiresAt, s.Aliases, sessionBinding{sessionID: s.SessionID, provider: s.Provider, sequence: s.Sequence})
		}
		c.dirty = false
		c.mu.Unlock()
	}
	go c.flushLoop()
	return c
}

func (c *SessionCache) flushLoop() {
	ticker := time.NewTicker(sessionCacheFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.flush()
		}
	}
}

func (c *SessionCache) flush() {
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return
	}
	c.dirty = false
	now := time.Now()
	saved := make([]persistedSession, 0, len(c.groups))
	for _, g := range c.groups {
		if now.Before(g.expiresAt) {
			saved = append(saved, persistedSession{AuthID: g.authID, ExpiresAt: g.expiresAt, Aliases: g.aliases, SessionID: g.binding.sessionID, Provider: g.binding.provider, Sequence: g.binding.sequence})
		}
	}
	c.mu.Unlock()
	data, err := json.Marshal(saved)
	if err == nil {
		tmp := c.statePath + ".tmp"
		if err = os.MkdirAll(filepath.Dir(c.statePath), 0o700); err == nil {
			if err = os.WriteFile(tmp, data, 0o600); err == nil {
				err = os.Rename(tmp, c.statePath)
			}
		}
	}
	if err != nil {
		log.Warnf("session affinity state not saved: %v", err)
		c.mu.Lock()
		c.dirty = true
		c.mu.Unlock()
	}
}

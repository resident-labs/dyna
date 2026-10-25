package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

// Cache matches successful calls by identity, never journal completion order.
// Ambiguous identities are deliberately left uncached so they can re-execute.
type Cache struct {
	mu         sync.Mutex
	current    map[string]any
	priorCalls int
	hits       int
}

// CacheStats summarizes replay use after a resumed run.
type CacheStats struct {
	Hits       int
	PriorCalls int
}

// NewCache builds a resume cache. Counts include errors and isolated calls:
// dropping holes before checking uniqueness could replay another job's result.
func NewCache(entries []runstore.JournalEntry) *Cache {
	c := &Cache{current: map[string]any{}, priorCalls: len(entries)}
	keys := map[string]int{}
	for _, e := range entries {
		keys[e.Key]++
	}
	for _, e := range entries {
		// Older keys cannot prove that the execution configuration matches.
		// Do not let a changed configuration fall back to a stale result.
		if !strings.HasPrefix(e.Key, "v3:") || keys[e.Key] != 1 || e.Error != "" || e.Dir != "" || e.Worktree != "" {
			continue
		}
		c.current[e.Key] = e.Result
	}
	return c
}

func (c *Cache) pop(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if result, ok := c.current[key]; ok {
		delete(c.current, key)
		c.hits++
		return result, true
	}
	return nil, false
}

// Stats returns the number of cache hits and prior journaled calls.
func (c *Cache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStats{Hits: c.hits, PriorCalls: c.priorCalls}
}

// executionConfigDigest hashes execution settings without persisting their values.
// Keep model order: fallback order is part of the execution configuration.
// Exclude descriptions, ratings, scheduling caps and generated worker context.
// Runtime preparation happens after lookup. This digest covers profile settings,
// not installed runtime bundles, active provider accounts or external state.
func executionConfigDigest(p profile.Profile, timeout time.Duration) string {
	config := struct {
		Harness string
		Models  []string
		Args    []string          `json:",omitempty"`
		Env     map[string]string `json:",omitempty"`
		// Keep the empty retired member in place to preserve existing replay keys.
		RetiredAccount    string `json:"RuntimeAccount"`
		RuntimeVersion    string
		RuntimeExecutable string
		RuntimeSkillPaths []string `json:",omitempty"`
		Dialect           string
		SafeMode          bool
		Timeout           time.Duration
	}{
		Harness:           p.Harness,
		Models:            p.EffectiveModels(),
		Args:              p.ExtraArgs,
		Env:               p.Env,
		RuntimeVersion:    p.RuntimeVersion,
		RuntimeExecutable: p.RuntimeExecutable,
		RuntimeSkillPaths: p.RuntimeSkillPaths,
		Dialect:           p.EffectiveDialect(),
		SafeMode:          p.SafeMode,
		Timeout:           timeout,
	}
	payload, _ := json.Marshal(config)
	h := sha256.Sum256(payload)
	return "exec-v1:" + hex.EncodeToString(h[:])
}

// callKey includes the effective configuration, cwd and explicit job label.
// Generated agent ordinals are excluded because call order can change on resume.
func callKey(p profile.Profile, prompt, schemaJSON, cwd, label string, timeout time.Duration) string {
	payload, _ := json.Marshal([]string{p.Name, prompt, schemaJSON, cwd, label, executionConfigDigest(p, timeout)})
	h := sha256.Sum256(payload)
	return "v3:" + hex.EncodeToString(h[:])
}

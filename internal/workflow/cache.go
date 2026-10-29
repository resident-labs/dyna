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
// Ambiguous agent and command identities stay uncached. Ambiguous jobs reject
// during replay because submitting another job could duplicate side effects.
type Cache struct {
	mu            sync.Mutex
	current       map[string]any
	execs         map[string]execRecord
	jobs          map[string]jobRecord
	ambiguousJobs map[string]bool
	priorCalls    int
	hits          int
}

// CacheStats summarizes replay use after a resumed run.
type CacheStats struct {
	Hits       int
	PriorCalls int
}

// NewCache builds a resume cache. Counts include errors and isolated calls:
// dropping holes before checking uniqueness could replay another job's result.
func NewCache(entries []runstore.JournalEntry) *Cache {
	c := &Cache{current: map[string]any{}, execs: map[string]execRecord{}, jobs: map[string]jobRecord{}, ambiguousJobs: map[string]bool{}}
	keys := map[string]int{}
	var execEntries, jobEntries []runstore.JournalEntry
	for _, e := range entries {
		switch e.Kind {
		case "exec":
			execEntries = append(execEntries, e)
			continue
		case "job":
			jobEntries = append(jobEntries, e)
			continue
		}
		c.priorCalls++
		keys[e.Key]++
	}
	c.addExecs(execEntries)
	c.addJobs(jobEntries)
	for _, e := range entries {
		if e.Kind == "exec" || e.Kind == "job" {
			continue
		}
		// Older keys cannot prove that the execution configuration matches.
		// Do not let a changed configuration fall back to a stale result.
		if !strings.HasPrefix(e.Key, "v3:") || keys[e.Key] != 1 || e.Error != "" || e.Dir != "" || e.Worktree != "" {
			continue
		}
		c.current[e.Key] = e.Result
	}
	return c
}

// addExecs keeps one record per run() call: its last journal entry. A call
// that succeeded gives its result; one that was still running gives its shell,
// so a resumed run waits for it instead of starting it again. A failed call
// runs again, like a failed agent. A key two calls share stays uncached.
func (c *Cache) addExecs(entries []runstore.JournalEntry) {
	last := map[int]runstore.JournalEntry{}
	for _, e := range entries {
		last[e.ID] = e
	}
	calls := map[string]int{}
	for _, e := range last {
		calls[e.Key]++
	}
	for _, e := range last {
		c.priorCalls++
		if calls[e.Key] != 1 || !strings.HasPrefix(e.Key, "x1:") || e.Error != "" {
			continue
		}
		switch e.State {
		case "done":
			c.execs[e.Key] = execRecord{Shell: e.Shell, Stderr: e.Stderr, Done: true, Result: e.Result}
		case "running":
			c.execs[e.Key] = execRecord{Shell: e.Shell, Stderr: e.Stderr}
		}
	}
}

// addJobs keeps one record per job() invocation. Its key includes the stable
// resume namespace, request hash, source and branch position, and occurrence.
// Identical parallel branches keep their own jobs.
// Any submitted job ID stays resumable after an interrupted wait.
func (c *Cache) addJobs(entries []runstore.JournalEntry) {
	calls := map[string]map[int]bool{}
	unsafe := map[string]bool{}
	for _, e := range entries {
		if e.RequestHash == "" || e.PositionHash == "" {
			continue
		}
		identity := e.RequestHash + "\n" + e.PositionHash
		if calls[identity] == nil {
			calls[identity] = map[int]bool{}
		}
		calls[identity][e.ID] = true
		unsafe[identity] = unsafe[identity] || e.JobReplayUnsafe
	}
	for identity, invocations := range calls {
		c.ambiguousJobs[identity] = len(invocations) > 1 || unsafe[identity]
	}
	last := map[int]runstore.JournalEntry{}
	for _, e := range entries {
		last[e.ID] = e
	}
	for _, e := range last {
		c.priorCalls++
		if !strings.HasPrefix(e.Key, "j4:") || e.Job == "" {
			continue
		}
		c.jobs[e.Key] = jobRecord{Job: e.Job, RequestHash: e.RequestHash, Done: e.State == "done", Result: e.Result}
	}
}

func (c *Cache) ambiguousJob(identity string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ambiguousJobs[identity]
}

func (c *Cache) popJob(key string) (jobRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record, ok := c.jobs[key]
	if ok {
		delete(c.jobs, key)
		c.hits++
	}
	return record, ok
}

func (c *Cache) popExec(key string) (execRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.execs[key]
	if ok {
		delete(c.execs, key)
		c.hits++
	}
	return rec, ok
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

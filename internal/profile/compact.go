package profile

import (
	"fmt"
	"time"
)

// claudeCompactBuffer is how far below autoCompactWindow Claude Code
// compacts. /context on Claude Code 2.1.302 showed a 33K "Autocompact
// buffer" for Haiku and Opus at windows of 100K, 130K and 400K. The window
// must be 100000 to 1000000.
const claudeCompactBuffer = 33000

var compactRules = map[string]CompactRule{
	HarnessClaudeCode: {Min: 100000 - claudeCompactBuffer, Max: 1000000 - claudeCompactBuffer},
	HarnessPi:         {Min: 1},
}

// CompactRuleFor returns the rule for harness.
func CompactRuleFor(harness string) CompactRule {
	if r, ok := compactRules[harness]; ok {
		return r
	}
	return CompactRule{Reason: "This harness has no compaction threshold setting."}
}

// checkCompact reports whether p.Compact suits model on p's harness and
// returns that model's window.
func checkCompact(p Profile, model string) (int64, error) {
	c, r := p.Compact, CompactRuleFor(p.Harness)
	window := c.Windows[model]
	switch {
	case r.Reason != "":
		return 0, fmt.Errorf("compact: %s", r.Reason)
	case r.Max > 0 && (c.Tokens < r.Min || c.Tokens > r.Max):
		return 0, fmt.Errorf("compact: %s takes %d to %d tokens (got %d)", p.Harness, r.Min, r.Max, c.Tokens)
	case c.Tokens < r.Min:
		return 0, fmt.Errorf("compact: %s takes at least %d tokens (got %d)", p.Harness, r.Min, c.Tokens)
	case r.Window && window <= c.Tokens:
		return 0, fmt.Errorf("compact: %s needs the context window of model %q above %d tokens (got %d)", p.Harness, model, c.Tokens, window)
	}
	return window, nil
}

// validateCompact checks p.Compact against every model of the chain, so a
// fallback model cannot get a threshold beyond its window.
func validateCompact(p Profile) error {
	if p.Compact == nil {
		return nil
	}
	models := p.EffectiveModels()
	if len(models) == 0 {
		models = []string{""}
	}
	for _, m := range models {
		p.Model = m
		if _, _, err := CompactNative(p); err != nil {
			return err
		}
	}
	return nil
}

// CompactNative returns the args and environment that set p.Compact on p's
// harness for p.Model, the model of this attempt. Every turn of every
// harness, fallbacks included, gets them through this one function.
func CompactNative(p Profile) ([]string, map[string]string, error) {
	c := p.Compact
	if c == nil {
		return nil, nil, nil
	}
	if _, err := checkCompact(p, p.Model); err != nil {
		return nil, nil, err
	}
	switch p.Harness {
	case HarnessPi:
		// The invocation passes the profile's target as
		// --compaction-trigger-tokens; see piInvocation.
		return nil, nil, nil
	case HarnessClaudeCode:
		// The harness merges this --settings layer with the profile's own.
		return []string{"--settings", fmt.Sprintf(`{"autoCompactWindow":%d}`, c.Tokens+claudeCompactBuffer)}, nil, nil
	}
	return nil, nil, fmt.Errorf("compact: %s has no mapping", p.Harness)
}

// CompactDue reports whether Dyna should run the harness's compact command
// before resuming a session whose last model call had contextTokens of prompt.
func CompactDue(p Profile, contextTokens int64) bool {
	return p.Compact != nil && CompactRuleFor(p.Harness).Trigger && contextTokens >= p.Compact.Tokens
}

// Idle compaction. A session whose prompt cache lapsed pays for its whole
// context again at the first model call of its next turn, and at every later
// call. The engine compacts an idle session once, after IdleCompactAfter, so
// that turn reads a small context. A session that never wakes again costs one
// summary call.
const (
	// IdleCompactAfter is how long a session sits idle before it is compacted.
	// It is one constant for every harness: Claude Code keeps its prompt cache
	// for 5 minutes on API keys and 1 hour on subscriptions, and 30 minutes
	// lands between them. t3code compacts idle sessions on the same idea.
	IdleCompactAfter = 30 * time.Minute
	// IdleCompactMinTokens is the smallest context worth a summary call.
	IdleCompactMinTokens = 100000
)

// IdleCompactDue reports whether an idle session of p with contextTokens in its
// last model call's prompt is worth compacting. Only Claude Code and Pi have a
// compact command.
func IdleCompactDue(p Profile, contextTokens int64) bool {
	return (p.Harness == HarnessClaudeCode || p.Harness == HarnessPi) && contextTokens >= IdleCompactMinTokens
}

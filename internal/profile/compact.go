package profile

import (
	"fmt"
)

// Compact asks the harness to compact its context once the context holds
// Tokens. Windows maps each model of the profile's chain to its context
// window, copied from the harness catalog when the profile is saved.
// Harnesses whose native setting is relative to the window need it, and a
// fallback model uses its own window.
type Compact struct {
	Tokens  int64            `json:"tokens"`
	Windows map[string]int64 `json:"windows,omitempty"`
}

// CompactRule is what one harness accepts for Compact. The profile editor
// reads it from the model catalog.
type CompactRule struct {
	// Window means the harness needs Compact.Windows for every model.
	Window bool  `json:"window,omitempty"`
	Min    int64 `json:"min,omitempty"`
	Max    int64 `json:"max,omitempty"`
	// Trigger means the harness has no threshold setting but has a compact
	// command. Dyna runs it before a resumed turn once the last model call's
	// prompt reached Tokens. See CompactDue.
	Trigger bool `json:"trigger,omitempty"`
	// Reason says why the harness has no native threshold for one run.
	// Dyna does not emulate compaction, so the editor disables the field.
	Reason string `json:"reason,omitempty"`
}

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
		// Pi's runtime generation writes each profile's target as a per-model
		// compaction.modelOverrides[<model>].triggerTokens entry.
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

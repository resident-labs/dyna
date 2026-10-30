package profile

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

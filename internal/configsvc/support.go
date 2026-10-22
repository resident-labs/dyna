package configsvc

import (
	"context"
	"dyna-agent/internal/harness/health"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

// Support states for one capability. They match workeroutput's usage values.
const (
	SupportYes         = "yes"
	SupportNo          = "no"
	SupportUnavailable = "unavailable"
)

// SupportItem is one capability of a profile's harness, as the editor lists it.
// Reason is plain words and is empty when State is "yes". Chip is the short
// label a profile row shows when this item is a gap.
type SupportItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	State  string `json:"state"`
	Reason string `json:"reason"`
	Chip   string `json:"chip"`
}

// HarnessSupport says how much of a harness's output Dyna can read. Partial
// is true when any item is not "yes". Dialect is the adapter that decodes it.
type HarnessSupport struct {
	health.View
	Installed    bool          `json:"installed"`
	Installable  bool          `json:"installable,omitempty"`
	Binary       string        `json:"binary"`
	VersionError string        `json:"versionError,omitempty"`
	Dialect      string        `json:"dialect"`
	Partial      bool          `json:"partial"`
	Items        []SupportItem `json:"items"`
}

// supportFor reads the native or explicitly declared output contract.
func supportFor(p profile.Profile) HarnessSupport {
	dialect := p.EffectiveDialect()
	c := workeroutput.Capabilities(dialect)
	name := dialect
	if dialect == workeroutput.DialectText {
		name = "plain text"
	}
	flag := func(key, label, chip string, ok bool, reason string) SupportItem {
		if ok {
			return SupportItem{Key: key, Label: label, State: SupportYes}
		}
		return SupportItem{Key: key, Label: label, State: SupportNo, Reason: reason, Chip: chip}
	}
	usage := SupportItem{Key: "usage", Label: "Token counts", State: c.Usage}
	if c.Usage != SupportYes {
		if c.Usage != SupportUnavailable {
			usage.State = SupportNo
		}
		usage.Chip = "No token counts"
		usage.Reason = c.UsageReason
		if usage.Reason == "" {
			usage.Reason = "The " + name + " output does not report token use"
		}
	}
	items := []SupportItem{
		flag("transcript", "Transcript", "No transcript", c.Transcript, "The "+name+" output has no readable messages"),
		flag("toolEvents", "Tool calls", "No tool calls", c.ToolEvents, "The "+name+" output does not show tool calls"),
		flag("safePoints", "Safe points", "No safe points", c.SafePoints, "The "+name+" output does not mark where a tool call ends, so a steering note waits and may interrupt"),
		usage,
		flag("sessionResume", "Session resume", "No resume", c.SessionResume, "The "+name+" output does not carry a session ID, so a turn cannot resume the same session"),
	}
	version := health.VersionDetector(context.Background(), p)
	out := HarnessSupport{Dialect: dialect, Items: items,
		View:      health.New("").View(p, version.Version),
		Installed: version.Installed, Installable: version.Installable, Binary: version.Path, VersionError: version.Error}
	for _, it := range items {
		if it.State != SupportYes {
			out.Partial = true
		}
	}
	return out
}

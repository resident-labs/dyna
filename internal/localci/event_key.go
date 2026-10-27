package localci

import "strings"

// Event key families on Topic. Publishers build keys here, and readers parse
// them here, so a new family does not add another prefix check in the engine.
const (
	modeEventKey      = "mode"
	nodeEventPrefix   = "node:"
	resultEventPrefix = "result:"
	cachesEventPrefix = "caches:"
	hostedEventPrefix = "hosted:"
	warmEventPrefix   = "warm:"
	attachEventPrefix = "attach:"
)

type eventKind int

const (
	eventMode eventKind = iota + 1
	eventNode
	eventResult
	eventCaches
	eventHosted
	eventWarm
	eventAttach
)

// EventKey is one committed key on the localci hub topic.
type EventKey struct {
	kind eventKind
	raw  string
}

// ModeEventKey is a room CI mode change.
func ModeEventKey() EventKey { return EventKey{kind: eventMode, raw: modeEventKey} }

// NodeEventKey is a CI node registry change. The engine still wakes on this
// family so an older event keeps its meaning.
func NodeEventKey(name string) EventKey {
	return EventKey{kind: eventNode, raw: nodeEventPrefix + name}
}

// ResultEventKey is a saved local CI result for one commit.
func ResultEventKey(sha string) EventKey {
	return EventKey{kind: eventResult, raw: resultEventPrefix + sha}
}

// CachesEventKey records a node cache cleanup. It does not wake CI workers.
func CachesEventKey(node string) EventKey {
	return EventKey{kind: eventCaches, raw: cachesEventPrefix + node}
}

// HostedEventKey records hosted CI runs for one commit. It does not wake CI workers.
func HostedEventKey(sha string) EventKey {
	return EventKey{kind: eventHosted, raw: hostedEventPrefix + sha}
}

// WarmEventKey records a node cache warm for one repository.
func WarmEventKey(repo, node string) EventKey {
	return EventKey{kind: eventWarm, raw: warmEventPrefix + repo + ":" + node}
}

// AttachEventKey records a detached check attachment for one commit.
func AttachEventKey(sha string) EventKey {
	return EventKey{kind: eventAttach, raw: attachEventPrefix + sha}
}

// String is the hub key. An unparsed key is empty.
func (k EventKey) String() string { return k.raw }

// WakesCI reports whether this key can change CI work. A mode change, a node
// change and a saved result can. Cache, warm, hosted and attach writes cannot.
func (k EventKey) WakesCI() bool {
	return k.kind == eventMode || k.kind == eventNode || k.kind == eventResult
}

// ResultSHA returns the commit of a result key, including an empty commit.
// A key that is not a result has ok false.
func (k EventKey) ResultSHA() (string, bool) {
	if k.kind != eventResult {
		return "", false
	}
	sha, _ := strings.CutPrefix(k.raw, resultEventPrefix)
	return sha, true
}

// ParseEventKey reads a localci hub key. It accepts every key the historical
// prefix checks accepted, including a family prefix with an empty tail.
func ParseEventKey(raw string) (EventKey, bool) {
	switch {
	case raw == modeEventKey:
		return ModeEventKey(), true
	case strings.HasPrefix(raw, nodeEventPrefix):
		return EventKey{kind: eventNode, raw: raw}, true
	case strings.HasPrefix(raw, resultEventPrefix):
		return EventKey{kind: eventResult, raw: raw}, true
	case strings.HasPrefix(raw, cachesEventPrefix):
		return EventKey{kind: eventCaches, raw: raw}, true
	case strings.HasPrefix(raw, hostedEventPrefix):
		return EventKey{kind: eventHosted, raw: raw}, true
	case strings.HasPrefix(raw, warmEventPrefix):
		return EventKey{kind: eventWarm, raw: raw}, true
	case strings.HasPrefix(raw, attachEventPrefix):
		return EventKey{kind: eventAttach, raw: raw}, true
	default:
		return EventKey{}, false
	}
}

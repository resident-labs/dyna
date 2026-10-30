package room

import "strings"

// SenderKindFor returns the stored kind or derives it for retained posts.
// TODO: legacy/shim remove only after the upgrade floor excludes posts without senderKind.
func (m Message) SenderKindFor(roomName string) SenderKind {
	if m.SenderKind != "" {
		return m.SenderKind
	}
	switch {
	case m.EngineEvent():
		return SenderEngine
	// A plain engine post, such as a close hand-off forward, names the engine
	// as its sender. In the room that shares the engine's name, only the
	// forward tells it apart from the owner.
	case m.From == EngineSender && !m.Human && (roomName != EngineSender || m.Forward != nil):
		return SenderEngine
	case m.Human || m.From == roomName:
		return SenderOwner
	case strings.HasPrefix(m.From, "run:"):
		return SenderWorkflow
	case m.Source == SourceVoice:
		return SenderVoice
	case strings.HasSuffix(m.From, ":root"):
		return SenderRoot
	default:
		return SenderSession
	}
}

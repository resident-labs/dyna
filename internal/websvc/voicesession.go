package websvc

import (
	"context"
	"net/http"

	"github.com/gorilla/websocket"

	"dyna-agent/internal/voice/agentwire"
)

// AgentVoiceStream is the agent's side of one live voice session: the lines it
// sends in and the lines it reads out.
type AgentVoiceStream interface {
	// Read returns the next line from the agent. An error ends the input.
	Read() (agentwire.InMessage, error)
	Write(agentwire.OutMessage) error
}

// AgentVoiceHost runs live voice sessions for agents.
type AgentVoiceHost interface {
	// Session runs one session for caller and returns when it ends. An error
	// means it never started; the route reports it on the stream.
	Session(ctx context.Context, caller Caller, stream AgentVoiceStream) error
}

// maxAgentVoiceMessage bounds one line from the agent: an image of
// agentwire.MaxImageBytes is about a third larger in base64.
const maxAgentVoiceMessage = 8 << 20

// voiceSession serves /api/voice/session to a command on the private Unix
// socket: JSON lines as WebSocket text frames. The route is for local
// callers only, because it spends the owner's voice account for the caller
// the engine identified.
func (s *Server) voiceSession(w http.ResponseWriter, r *http.Request) {
	if !localRequest(r) {
		jsonError(w, http.StatusForbidden, "agent voice sessions are only for commands on this machine")
		return
	}
	if s.o.AgentVoice == nil {
		jsonError(w, http.StatusNotFound, "agent voice sessions are not available")
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	caller, ok := CallerFromContext(r.Context())
	if !ok {
		jsonError(w, http.StatusForbidden, "the engine cannot identify this caller")
		return
	}
	ws, err := voiceUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade has already answered
	}
	defer ws.Close()
	ws.SetReadLimit(maxAgentVoiceMessage)
	stream := agentVoiceSocket{ws}
	if err := s.o.AgentVoice.Session(r.Context(), caller, stream); err != nil {
		_ = stream.Write(agentwire.OutMessage{Type: agentwire.OutError, Message: err.Error()})
		_ = stream.Write(agentwire.OutMessage{Type: agentwire.OutClosed, Reason: agentwire.EndError})
	}
	_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

// agentVoiceSocket carries the session's lines as JSON text frames.
type agentVoiceSocket struct{ ws *websocket.Conn }

func (a agentVoiceSocket) Read() (agentwire.InMessage, error) {
	var m agentwire.InMessage
	err := a.ws.ReadJSON(&m)
	return m, err
}

func (a agentVoiceSocket) Write(m agentwire.OutMessage) error { return a.ws.WriteJSON(m) }

package dynamcp

// Streamable HTTP framing follows T3 Code's McpHttpServer.ts at 15fe283 (MIT).
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// MarshalJSON keeps "text" on an empty text block, as MCP requires. Actions that print nothing return one.
func (c content) MarshalJSON() ([]byte, error) {
	if c.Type == "text" {
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{c.Type, c.Text})
	}
	type block content
	return json.Marshal(block(c))
}

type result struct {
	Content           []content       `json:"content"`
	IsError           bool            `json:"isError,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
}
type callParams struct {
	Name      string                     `json:"name"`
	Arguments map[string]json.RawMessage `json:"arguments"`
	Meta      struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	} `json:"_meta"`
}

// Forward makes a single box-socket API request and returns its typed error unchanged.
type Forward func(context.Context, contract.Method, []byte) ([]byte, *contract.Error)
type Local func(context.Context, tools.BoxHandler, []byte) (result, *contract.Error)

type Server struct {
	list     []tool
	names    string
	actions  map[string]map[string]action
	forward  Forward
	local    Local
	mu       sync.Mutex
	pending  map[string]context.CancelFunc
	preparer post.Preparer
}

func New(forward Forward, local Local, preparer post.Preparer) (*Server, error) {
	list, actions, err := registry()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list))
	for _, tool := range list {
		names = append(names, tool.Name)
	}
	return &Server{list: list, names: strings.Join(names, ", "), actions: actions, forward: forward, local: local, pending: make(map[string]context.CancelFunc), preparer: preparer}, nil
}

// Description names the areas from the same registry as tools/list.
func (s *Server) Description() string { return "Dyna tools cover " + s.names + "." }

func marshal[T any](v T) json.RawMessage { data, _ := json.Marshal(v); return data }

func failed(problem contract.Error) result {
	detail := marshal(problem)
	return result{Content: []content{{Type: "text", Text: string(detail)}}, IsError: true, StructuredContent: detail}
}

func failure(code contract.ErrorCode, message string) result {
	return failed(contract.Error{Code: code, Message: message})
}

func (s *Server) call(ctx context.Context, p callParams) result {
	verbs, ok := s.actions[p.Name]
	if !ok {
		return failure(contract.ErrUnknownAction, "Unknown tool. Valid tools: "+s.names)
	}
	var verb string
	if err := json.Unmarshal(p.Arguments["action"], &verb); err != nil {
		return failure(contract.ErrInvalid, "Provide an action from the tool schema.")
	}
	a, ok := verbs[verb]
	if !ok {
		var names []string
		for name := range verbs {
			names = append(names, name)
		}
		sort.Strings(names)
		return failure(contract.ErrUnknownAction, "Unknown action. Valid actions: "+strings.Join(names, ", "))
	}
	delete(p.Arguments, "action")
	var callID string
	if raw := p.Arguments[contract.ToolCallIDArgument]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &callID); err != nil || len(callID) > 4096 {
			return failure(contract.ErrInvalid, "invalid tool call ID")
		}
		delete(p.Arguments, contract.ToolCallIDArgument)
	}
	var clientID json.RawMessage
	if a.op != "" {
		clientID = p.Arguments["clientId"]
		delete(p.Arguments, "clientId")
	}
	data, err := json.Marshal(p.Arguments)
	if err != nil {
		return failure(contract.ErrInvalid, err.Error())
	}
	decoded := reflect.New(a.request).Interface()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(decoded); err != nil {
		return failure(contract.ErrInvalid, err.Error())
	}
	if a.method == contract.MethodPost {
		if s.preparer.Upload == nil {
			return failure(contract.ErrUnavailable, "Post preparation is unavailable.")
		}
		req, checks, err := s.preparer.Prepare(ctx, *decoded.(*tools.PostInput))
		if err != nil {
			return failure(contract.ErrInvalid, err.Error())
		}
		req.ToolCallID = callID
		out, problem := s.forward(ctx, a.method, marshal(req))
		if problem != nil {
			return failed(*problem)
		}
		var response contract.PostResponse
		if err := json.Unmarshal(out, &response); err != nil {
			return failure(contract.ErrUnavailable, "Invalid post response.")
		}
		output := marshal(struct {
			contract.PostResponse
			Checks []contract.VisualCheck `json:"checks,omitempty"`
		}{response, checks})
		return result{Content: []content{{Type: "text", Text: string(output)}}, StructuredContent: output}
	}

	if a.method == contract.MethodSkillSave {
		in := decoded.(*tools.SkillSaveInput)
		req, err := skills.Request(in.Path, s.preparer.Dir, in.Room, in.Global)
		if err != nil {
			return failure(contract.ErrInvalid, err.Error())
		}
		data = marshal(req)
	}

	if a.local != tools.NoBoxHandler {
		if s.local == nil {
			return failure(contract.ErrUnavailable, "Box tool is unavailable.")
		}
		out, problem := s.local(ctx, a.local, data)
		if problem != nil {
			return failed(*problem)
		}
		return out
	}
	if a.op != "" {
		type op struct {
			Kind contract.OpKind `json:"kind"`
			Room contract.RoomID `json:"room"`
			Args json.RawMessage `json:"args"`
		}
		type submit struct {
			Operation op              `json:"operation"`
			ClientID  json.RawMessage `json:"clientId,omitempty"`
		}
		data = marshal(submit{Operation: op{Kind: a.op, Args: data}, ClientID: clientID})
	}
	out, problem := s.forward(ctx, a.method, data)
	if problem != nil {
		return failed(*problem)
	}
	return result{Content: []content{{Type: "text", Text: string(out)}}, StructuredContent: out}
}

func writeEvent[T any](w http.ResponseWriter, value T) {
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", marshal(value))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func sendProgress(w http.ResponseWriter, token json.RawMessage, count int) {
	notice := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  struct {
			ProgressToken json.RawMessage `json:"progressToken"`
			Progress      int             `json:"progress"`
		} `json:"params"`
	}{JSONRPC: "2.0", Method: "notifications/progress"}
	notice.Params.ProgressToken, notice.Params.Progress = token, count
	writeEvent(w, notice)
}

func (s *Server) stream(ctx context.Context, w http.ResponseWriter, p callParams) result {
	sendProgress(w, p.Meta.ProgressToken, 0)
	done := make(chan result, 1)
	go func() { done <- s.call(ctx, p) }()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	progress := 0
	for {
		select {
		case out := <-done:
			return out
		case <-ctx.Done():
			return failure(contract.ErrUnavailable, ctx.Err().Error())
		case <-tick.C:
			progress++
			sendProgress(w, p.Meta.ProgressToken, progress)
		}
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "Parse error."}})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32600, "Invalid request."}})
		return
	}
	if len(req.ID) == 0 {
		if req.Method == "notifications/cancelled" {
			var p struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			if json.Unmarshal(req.Params, &p) == nil {
				s.mu.Lock()
				cancel := s.pending[string(p.RequestID)]
				s.mu.Unlock()
				if cancel != nil {
					cancel()
				}
			}
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	resp := response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		resp.Result = marshal(struct {
			ProtocolVersion string `json:"protocolVersion"`
			Capabilities    struct {
				Tools struct{} `json:"tools"`
			} `json:"capabilities"`
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
			Instructions string `json:"instructions"`
		}{ProtocolVersion: p.ProtocolVersion, Instructions: s.Description(), ServerInfo: struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}{"dyna", "1"}})
	case "ping":
		resp.Result = json.RawMessage(`{}`)
	case "tools/list":
		resp.Result = marshal(struct {
			Tools []tool `json:"tools"`
		}{s.list})
	case "tools/call":
		var p callParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Arguments == nil {
			resp.Error = &rpcError{-32602, "Invalid tool call."}
			break
		}
		ctx, cancel := context.WithTimeout(r.Context(), 600*time.Second)
		s.mu.Lock()
		s.pending[string(req.ID)] = cancel
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.pending, string(req.ID))
			s.mu.Unlock()
			cancel()
		}()
		if len(p.Meta.ProgressToken) > 0 && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			resp.Result = marshal(s.stream(ctx, w, p))
			writeEvent(w, resp)
			return
		}
		resp.Result = marshal(s.call(ctx, p))
	default:
		resp.Error = &rpcError{-32601, "Method not found."}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

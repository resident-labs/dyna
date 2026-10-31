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
	list    []tool
	actions map[string]map[string]action
	forward Forward
	local   Local
	mu      sync.Mutex
	pending map[string]context.CancelFunc
}

func New(forward Forward, local Local) (*Server, error) {
	list, actions, err := registry()
	if err != nil {
		return nil, err
	}
	return &Server{list: list, actions: actions, forward: forward, local: local, pending: make(map[string]context.CancelFunc)}, nil
}

// Description names the areas from the same registry as tools/list.
func (s *Server) Description() string {
	names := make([]string, 0, len(s.list))
	for _, tool := range s.list {
		names = append(names, tool.Name)
	}
	return "Dyna tools cover " + strings.Join(names, ", ") + "."
}

func marshal[T any](v T) json.RawMessage { data, _ := json.Marshal(v); return data }

func failure(code contract.ErrorCode, message string) result {
	detail := contract.Error{Code: code, Message: message}
	return result{Content: []content{{Type: "text", Text: string(marshal(detail))}}, IsError: true, StructuredContent: marshal(detail)}
}

func (s *Server) call(ctx context.Context, p callParams) result {
	verbs, ok := s.actions[p.Name]
	if !ok {
		var names []string
		for _, tool := range s.list {
			names = append(names, tool.Name)
		}
		return failure(contract.ErrUnknownAction, "Unknown tool. Valid tools: "+strings.Join(names, ", "))
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
	if a.local != tools.NoBoxHandler {
		if s.local == nil {
			return failure(contract.ErrUnavailable, "Box tool is unavailable.")
		}
		out, problem := s.local(ctx, a.local, data)
		if problem != nil {
			return result{Content: []content{{Type: "text", Text: string(marshal(problem))}}, IsError: true, StructuredContent: marshal(problem)}
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
		return result{Content: []content{{Type: "text", Text: string(marshal(problem))}}, IsError: true, StructuredContent: marshal(problem)}
	}
	return result{Content: []content{{Type: "text", Text: string(out)}}, StructuredContent: out}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
		}{ProtocolVersion: p.ProtocolVersion, ServerInfo: struct {
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
		stream := len(p.Meta.ProgressToken) > 0 && strings.Contains(r.Header.Get("Accept"), "text/event-stream")
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			progress := struct {
				JSONRPC string `json:"jsonrpc"`
				Method  string `json:"method"`
				Params  struct {
					ProgressToken json.RawMessage `json:"progressToken"`
					Progress      int             `json:"progress"`
				} `json:"params"`
			}{JSONRPC: "2.0", Method: "notifications/progress"}
			progress.Params.ProgressToken = p.Meta.ProgressToken
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", marshal(progress))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if stream {
			done := make(chan result, 1)
			go func() { done <- s.call(ctx, p) }()
			tick := time.NewTicker(30 * time.Second)
			defer tick.Stop()
			progress := 0
			for {
				select {
				case out := <-done:
					resp.Result = marshal(out)
					goto complete
				case <-ctx.Done():
					resp.Result = marshal(failure(contract.ErrUnavailable, ctx.Err().Error()))
					goto complete
				case <-tick.C:
					progress++
					notice := struct {
						JSONRPC string `json:"jsonrpc"`
						Method  string `json:"method"`
						Params  struct {
							ProgressToken json.RawMessage `json:"progressToken"`
							Progress      int             `json:"progress"`
						} `json:"params"`
					}{JSONRPC: "2.0", Method: "notifications/progress"}
					notice.Params.ProgressToken, notice.Params.Progress = p.Meta.ProgressToken, progress
					_, _ = fmt.Fprintf(w, "event: message\\ndata: %s\\n\\n", marshal(notice))
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
				}
			}
		}
		resp.Result = marshal(s.call(ctx, p))
	complete:
		if stream {
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", marshal(resp))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
	default:
		resp.Error = &rpcError{-32601, "Method not found."}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

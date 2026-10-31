package shell

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolCall struct {
	Name      string `json:"name"`
	CallID    string `json:"-"`
	Arguments struct {
		Command     string              `json:"command"`
		Description string              `json:"description"`
		Cwd         string              `json:"cwd"`
		Job         contract.ShellJobID `json:"job"`
	} `json:"arguments"`
}

const toolList = `{"tools":[
{"name":"shell","description":"Run a bash command in the box. It runs in the foreground for up to 30 s. If it is still running then, it keeps running in the background and this returns its job ID. Dyna wakes you when a background job ends.","inputSchema":{"type":"object","properties":{"command":{"type":"string"},"description":{"type":"string"},"cwd":{"type":"string"}},"required":["command"]}},
{"name":"shell_output","description":"Read new output from a job.","inputSchema":{"type":"object","properties":{"job":{"type":"string"}},"required":["job"]}},
{"name":"shell_stop","description":"Stop a job and its whole process group.","inputSchema":{"type":"object","properties":{"job":{"type":"string"}},"required":["job"]}},
{"name":"shell_jobs","description":"List the jobs.","inputSchema":{"type":"object","properties":{}}}]}`

// Handler serves MCP over Streamable HTTP in its stateless form: one JSON reply per POST.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed) // Pi then opens no server-to-client stream
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted) // a notification
			return
		}
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &params)
			resp.Result = json.RawMessage(fmt.Sprintf(`{"protocolVersion":%q,"capabilities":{"tools":{}},"serverInfo":{"name":"dyna-shell","version":"1"}}`, params.ProtocolVersion))
		case "ping":
			resp.Result = json.RawMessage(`{}`)
		case "tools/list":
			resp.Result = json.RawMessage(toolList)
		case "tools/call":
			var call toolCall
			if err := json.Unmarshal(req.Params, &call); err != nil {
				resp.Error = &rpcError{-32602, err.Error()}
				break
			}
			var args struct {
				Arguments map[string]json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &args); err != nil {
				resp.Error = &rpcError{-32602, err.Error()}
				break
			}
			if raw := args.Arguments[contract.ToolCallIDArgument]; len(raw) > 0 {
				if err := json.Unmarshal(raw, &call.CallID); err != nil || len(call.CallID) > 4096 {
					resp.Error = &rpcError{-32602, "invalid tool call ID"}
					break
				}
			}
			text, err := s.call(r.Context().Done(), call)
			if err != nil {
				text = err.Error()
			}
			resp.Result, err = json.Marshal(toolResult{Content: []textContent{{"text", text}}, IsError: err != nil})
			if err != nil {
				resp.Error = &rpcError{-32603, err.Error()}
			}
		default:
			resp.Error = &rpcError{-32601, "method not found: " + req.Method}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

func (s *Server) call(cancel <-chan struct{}, call toolCall) (string, error) {
	args := call.Arguments
	switch call.Name {
	case "shell":
		if strings.TrimSpace(args.Command) == "" {
			return "", fmt.Errorf("command is required")
		}
		j, err := s.run(cancel, args.Command, args.Cwd, args.Description, call.CallID)
		if err != nil {
			return "", err
		}
		return describe(j), nil
	case "shell_output", "shell_stop":
		j, ok := s.find(args.Job)
		if !ok {
			return "", fmt.Errorf("unknown job %q", args.Job)
		}
		if call.Name == "shell_stop" {
			s.stop(j)
		}
		return describe(j), nil
	case "shell_jobs":
		s.mu.Lock()
		defer s.mu.Unlock()
		var lines []string
		for _, j := range s.jobs {
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s", j.id, j.status(), Mask(j.command)))
		}
		return strings.Join(lines, "\n"), nil
	}
	return "", fmt.Errorf("unknown tool %q", call.Name)
}

// describe returns the unread output and the job state.
func describe(j *job) string {
	text, running := j.unread()
	if running {
		return fmt.Sprintf("%s\n[job %s is running in the background]", text, j.id)
	}
	return fmt.Sprintf("%s\n[job %s: %s]", text, j.id, j.status())
}

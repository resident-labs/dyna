package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
)

type rpcID struct {
	text    string
	number  bool
	present bool
}

func (id *rpcID) UnmarshalJSON(data []byte) error {
	id.present = true
	if len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, &id.text)
	}
	id.number = true
	id.text = string(data)
	return nil
}
func (id rpcID) MarshalJSON() ([]byte, error) {
	if id.number {
		return []byte(id.text), nil
	}
	return json.Marshal(id.text)
}

type shellRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      rpcID  `json:"id"`
	Method  string `json:"method"`
	Params  struct {
		Name      string `json:"name"`
		Arguments struct {
			Command string  `json:"command"`
			Cwd     string  `json:"cwd"`
			Node    *string `json:"node"`
		} `json:"arguments"`
	} `json:"params"`
}

type shellText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type shellCallResult struct {
	Content []shellText `json:"content"`
	IsError bool        `json:"isError,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type toolProperty struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}
type toolSchema struct {
	Type       string `json:"type"`
	Properties struct {
		Command toolProperty `json:"command"`
		Cwd     toolProperty `json:"cwd"`
	} `json:"properties"`
	Required             []string `json:"required"`
	AdditionalProperties bool     `json:"additionalProperties"`
}
type shellTool struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	InputSchema toolSchema `json:"inputSchema"`
}
type shellToolList struct {
	Tools []shellTool `json:"tools"`
}
type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type shellCapabilities struct {
	Tools struct{} `json:"tools"`
}
type initializeResult struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    shellCapabilities `json:"capabilities"`
	ServerInfo      serverInfo        `json:"serverInfo"`
}

type ShellExecutor interface {
	Run(context.Context, string, string) (string, error)
}

type GuardedShell struct{ Guard *Guard }

func (s GuardedShell) Run(_ context.Context, command, cwd string) (string, error) {
	var stdout, stderr bytes.Buffer
	job, err := s.Guard.Start(command, cwd, &stdout, &stderr)
	if err == nil {
		err = job.Wait()
	}
	return stdout.String() + stderr.String(), err
}

// Shell serves the box-local MCP shell over newline-delimited JSON-RPC.
func Shell(ctx context.Context, executor ShellExecutor, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	encoder := json.NewEncoder(output)
	var writeMu sync.Mutex
	write := func(v interface{ MarshalJSON() ([]byte, error) }) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return encoder.Encode(v)
	}
	for scanner.Scan() {
		var req shellRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			return err
		}
		if !req.ID.present {
			continue
		}
		switch req.Method {
		case "initialize":
			result := initializeResult{ProtocolVersion: "2025-03-26", ServerInfo: serverInfo{Name: "dyna-shell", Version: "1"}}
			if err := write(envelope[initializeResult]{ID: req.ID, Result: result}); err != nil {
				return err
			}
		case "tools/list":
			schema := toolSchema{Type: "object", Required: []string{"command"}}
			schema.Properties.Command = toolProperty{Type: "string", Description: "Shell command to run locally"}
			schema.Properties.Cwd = toolProperty{Type: "string", Description: "Working directory"}
			result := shellToolList{Tools: []shellTool{{Name: "shell", Description: "Run a command in this box.", InputSchema: schema}}}
			if err := write(envelope[shellToolList]{ID: req.ID, Result: result}); err != nil {
				return err
			}
		case "tools/call":
			if req.Params.Name != "shell" || req.Params.Arguments.Command == "" || req.Params.Arguments.Node != nil {
				if err := write(envelope[shellCallResult]{ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid shell arguments"}}); err != nil {
					return err
				}
				continue
			}
			text, err := executor.Run(ctx, req.Params.Arguments.Command, req.Params.Arguments.Cwd)
			if err != nil {
				text += "\n" + err.Error()
			}
			result := shellCallResult{Content: []shellText{{Type: "text", Text: text}}, IsError: err != nil}
			if err := write(envelope[shellCallResult]{ID: req.ID, Result: result}); err != nil {
				return err
			}
		default:
			if err := write(envelope[shellCallResult]{ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found"}}); err != nil {
				return err
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return scanner.Err()
}

// envelope has a typed result for each supported JSON-RPC method.
type envelope[T interface {
	shellCallResult | shellToolList | initializeResult
}] struct {
	ID     rpcID
	Result T
	Error  *rpcError
}

func (e envelope[T]) MarshalJSON() ([]byte, error) {
	if e.Error != nil {
		return json.Marshal(struct {
			JSONRPC string    `json:"jsonrpc"`
			ID      rpcID     `json:"id"`
			Error   *rpcError `json:"error"`
		}{"2.0", e.ID, e.Error})
	}
	return json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      rpcID  `json:"id"`
		Result  T      `json:"result"`
	}{"2.0", e.ID, e.Result})
}

package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
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
			Command        string  `json:"command"`
			Cwd            string  `json:"cwd"`
			Node           *string `json:"node"`
			TimeoutSeconds int     `json:"timeoutSeconds"`
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
		Command        toolProperty `json:"command"`
		Cwd            toolProperty `json:"cwd"`
		TimeoutSeconds toolProperty `json:"timeoutSeconds"`
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
	Run(context.Context, string, string, int) (string, error)
}

type GuardedShell struct{ Guard *Guard }

type boundedOutput struct {
	mu        sync.Mutex
	data      bytes.Buffer
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := 64*1024 - b.data.Len()
	if remaining < len(p) {
		b.truncated = true
		p = p[:remaining]
	}
	_, _ = b.data.Write(p)
	return n, nil
}

func (s GuardedShell) Run(ctx context.Context, command, cwd string, timeoutSeconds int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	var output boundedOutput
	job, err := s.Guard.Start(command, cwd, &output, &output)
	if err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- job.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = job.Stop()
		<-done
		err = ctx.Err()
	}
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			code = -1
		}
	}
	text := fmt.Sprintf("Exit code %d.\n%s", code, output.data.String())
	if output.truncated {
		text += "\n[Output truncated at 64 KiB.]"
	}
	return text, err
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
			schema.Properties.TimeoutSeconds = toolProperty{Type: "integer", Description: "Timeout in seconds; defaults to 600"}
			result := shellToolList{Tools: []shellTool{{Name: "shell", Description: "Run a command in this box.", InputSchema: schema}}}
			if err := write(envelope[shellToolList]{ID: req.ID, Result: result}); err != nil {
				return err
			}
		case "tools/call":
			if req.Params.Name != "shell" || req.Params.Arguments.Command == "" || req.Params.Arguments.Node != nil || req.Params.Arguments.TimeoutSeconds < 0 {
				if err := write(envelope[shellCallResult]{ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid shell arguments"}}); err != nil {
					return err
				}
				continue
			}
			timeout := req.Params.Arguments.TimeoutSeconds
			if timeout == 0 {
				timeout = 600
			}
			text, err := executor.Run(ctx, req.Params.Arguments.Command, req.Params.Arguments.Cwd, timeout)
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

package adapters

import (
	"bufio"
	"context"
	"dyna-agent/internal/childenv"
	"encoding/json"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// codexRPC only starts the app-server and performs read-only protocol requests.
// It never creates a thread, starts inference, or requests credential refresh.
// Wire evidence: T3 packages/effect-codex-app-server/src/_generated/schema.gen.ts.
type codexRPC struct {
	wireVersion    string
	accountUpdated bool
	cmd            *exec.Cmd
	input          io.WriteCloser
	output         io.ReadCloser
	scan           *bufio.Scanner
	next           int
	stop           func() bool
	cancel         context.CancelFunc
}

func openDiscoveryRPC(ctx context.Context, executable string, env []string, args ...string) (*codexRPC, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 200 * time.Millisecond
	cmd.Stderr = io.Discard
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, ErrProbe
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		cancel()
		return nil, ErrProbe
	}
	if err = childenv.Clean(cmd).Start(); err != nil {
		input.Close()
		output.Close()
		cancel()
		return nil, ErrProbe
	}
	rpc := &codexRPC{cmd: cmd, input: input, output: output, cancel: cancel}
	rpc.stop = context.AfterFunc(ctx, func() { input.Close(); output.Close() })
	rpc.scan = bufio.NewScanner(io.LimitReader(output, 4<<20))
	rpc.scan.Buffer(make([]byte, 4096), 1<<20)
	return rpc, nil
}
func openCodexRPC(ctx context.Context, executable string, env []string) (*codexRPC, error) {
	rpc, err := openDiscoveryRPC(ctx, executable, env, "app-server")
	if err != nil {
		return nil, err
	}
	input := rpc.input
	if err = rpc.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "dyna_account_discovery", "version": "1"}}, nil); err != nil {
		rpc.close()
		return nil, err
	}
	if json.NewEncoder(input).Encode(map[string]any{"method": "initialized"}) != nil {
		rpc.close()
		return nil, ErrProbe
	}
	return rpc, nil
}
func (r *codexRPC) close() {
	r.stop()
	r.cancel()
	r.input.Close()
	r.output.Close()
	_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
	_ = r.cmd.Process.Kill()
	_ = r.cmd.Wait()
}
func (r *codexRPC) call(method string, params any, result any) error {
	r.next++
	request := map[string]any{"id": r.next, "method": method}
	if params != nil {
		request["params"] = params
	}
	if r.wireVersion != "" {
		request["jsonrpc"] = r.wireVersion
	}
	if json.NewEncoder(r.input).Encode(request) != nil {
		return ErrProbe
	}
	for frames := 0; frames < 256 && r.scan.Scan(); frames++ {
		var msg struct {
			Version string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result"`
			Error   json.RawMessage `json:"error"`
			Method  string          `json:"method"`
		}
		if json.Unmarshal(r.scan.Bytes(), &msg) != nil {
			return ErrProbe
		}
		// Notifications cannot establish a response, and server requests are not run.
		if r.wireVersion != "" && msg.Version != r.wireVersion {
			return ErrProbe
		}
		if msg.Method != "" {
			if msg.Method == "account/updated" || msg.Method == "account/changed" {
				r.accountUpdated = true
			}
			continue
		}
		var id int
		if json.Unmarshal(msg.ID, &id) != nil || id != r.next {
			return ErrProbe
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			var failure struct {
				Code int `json:"code"`
			}
			_ = json.Unmarshal(msg.Error, &failure)
			if failure.Code == -32601 || failure.Code == -32010 {
				return ErrUnsupported
			}
			return ErrProbe
		}
		if len(msg.Result) == 0 || msg.Result[0] != '{' {
			return ErrProbe
		}
		if result != nil && json.Unmarshal(msg.Result, result) != nil {
			return ErrProbe
		}
		return nil
	}
	return ErrProbe
}

func modelsCodex(ctx context.Context, executable string, env []string) ([]Model, error) {
	rpc, err := openCodexRPC(ctx, executable, env)
	if err != nil {
		return nil, err
	}
	defer rpc.close()
	out := []Model{}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 32; page++ {
		var response struct {
			Data *[]struct {
				ID          string `json:"id"`
				Model       string `json:"model"`
				Name        string `json:"displayName"`
				Description string `json:"description"`
				Hidden      bool   `json:"hidden"`
				Efforts     []struct {
					Effort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
				DefaultEffort string `json:"defaultReasoningEffort"`
			} `json:"data"`
			Next string `json:"nextCursor"`
		}
		params := map[string]any{"limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err = rpc.call("model/list", params, &response); err != nil {
			return nil, err
		}
		if response.Data == nil {
			return nil, ErrProbe
		}
		for _, row := range *response.Data {
			id := row.Model
			if id == "" {
				id = row.ID
			}
			if id == "" || row.Hidden || seen[id] {
				continue
			}
			seen[id] = true
			name := row.Name
			if name == "" {
				name = id
			}
			m := Model{ID: id, Name: name, Provider: "openai", Description: row.Description, Source: "Codex app-server model/list"}
			for _, e := range row.Efforts {
				if e.Effort != "" {
					m.Efforts = append(m.Efforts, Effort{ID: e.Effort, Default: e.Effort == row.DefaultEffort, Args: []string{"-c", "model_reasoning_effort=" + e.Effort}})
				}
			}
			out = append(out, m)
		}
		if response.Next == "" {
			return out, nil
		}
		if response.Next == cursor {
			return nil, ErrProbe
		}
		cursor = response.Next
	}
	return nil, ErrProbe
}

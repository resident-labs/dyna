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

// discoveryRPC speaks one harness's read-only JSON-RPC discovery protocol. It
// never starts a turn, runs inference, or requests a credential refresh.
type discoveryRPC struct {
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

func openDiscoveryRPC(ctx context.Context, executable string, env []string, args ...string) (*discoveryRPC, error) {
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
	rpc := &discoveryRPC{cmd: cmd, input: input, output: output, cancel: cancel}
	rpc.stop = context.AfterFunc(ctx, func() { input.Close(); output.Close() })
	rpc.scan = bufio.NewScanner(io.LimitReader(output, 4<<20))
	rpc.scan.Buffer(make([]byte, 4096), 1<<20)
	return rpc, nil
}

func (r *discoveryRPC) close() {
	r.stop()
	r.cancel()
	r.input.Close()
	r.output.Close()
	_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
	_ = r.cmd.Process.Kill()
	_ = r.cmd.Wait()
}
func (r *discoveryRPC) call(method string, params any, result any) error {
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

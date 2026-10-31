package dynamcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/box/computer"
	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
)

// BoxTools runs the actions which live in the box instead of the engine.
func BoxTools(socket string) Local {
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	return func(ctx context.Context, handler tools.BoxHandler, raw []byte) (result, *contract.Error) {
		fail := func(err error) (result, *contract.Error) {
			return result{}, &contract.Error{Code: contract.ErrInvalid, Message: err.Error()}
		}
		var args []string
		var action computer.Action
		switch handler {
		case tools.VisualCheck:
			var req tools.FileRequest
			_ = json.Unmarshal(raw, &req)
			file, err := os.Open(req.File)
			if err != nil {
				return fail(err)
			}
			data, err := io.ReadAll(io.LimitReader(file, 25<<20+1))
			_ = file.Close()
			if err != nil {
				return fail(err)
			}
			if len(data) > 25<<20 {
				return fail(fmt.Errorf("visual exceeds 25 MiB"))
			}
			if _, _, err := visual.Meta(req.File, data); err != nil {
				return fail(err)
			}
			checked, err := visual.Inspect(ctx, data, visual.CSP(false), false)
			if err != nil {
				return fail(err)
			}
			output := marshal(checked.Checks)
			return result{Content: []content{{Type: "text", Text: string(output)}}, StructuredContent: output}, nil
		case tools.ComputerOpen:
			var req tools.OpenRequest
			_ = json.Unmarshal(raw, &req)
			action = computer.Open
			args = []string{req.URL}
		case tools.ComputerClick, tools.ComputerText:
			var req tools.SelectorRequest
			_ = json.Unmarshal(raw, &req)
			if req.Selector != "" {
				args = []string{req.Selector}
			}
			if handler == tools.ComputerClick {
				if req.Selector == "" {
					return fail(fmt.Errorf("selector is required"))
				}
				action = computer.Click
			} else {
				action = computer.Text
			}
		case tools.ComputerType:
			var req tools.TypeRequest
			_ = json.Unmarshal(raw, &req)
			if req.Selector == "" {
				return fail(fmt.Errorf("selector is required"))
			}
			action = computer.Type
			args = []string{req.Selector, req.Text}
		case tools.ComputerScreenshot:
			dir, err := os.MkdirTemp("", "dyna-mcp-shot-")
			if err != nil {
				return fail(err)
			}
			defer os.RemoveAll(dir)
			action = computer.Screenshot
			args = []string{filepath.Join(dir, "shot.png")}
		default:
			return fail(fmt.Errorf("unknown box action"))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dyna/computer", nil)
		if err != nil {
			return fail(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fail(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fail(fmt.Errorf("computer start: %s", resp.Status))
		}
		var start struct {
			URL string `json:"url"`
		}
		if err = json.NewDecoder(resp.Body).Decode(&start); err != nil {
			return fail(err)
		}
		dev, err := computer.Connect(ctx, start.URL)
		if err != nil {
			return fail(err)
		}
		defer dev.Close()
		var output bytes.Buffer
		if err = computer.Execute(ctx, dev, action, args, "", false, &output); err != nil {
			return fail(err)
		}
		if action == computer.Screenshot {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return fail(err)
			}
			return result{Content: []content{{Type: "image", Data: base64.StdEncoding.EncodeToString(data), MimeType: "image/png"}}}, nil
		}
		return result{Content: []content{{Type: "text", Text: output.String()}}}, nil
	}
}

package dynamcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/resident-labs/dyna/box/computer"
	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
)

// computerActions maps the computer tool's handlers to the box's actions.
var computerActions = map[tools.BoxHandler]computer.Action{
	tools.ComputerOpen: computer.Open, tools.ComputerClick: computer.Click, tools.ComputerMove: computer.Move, tools.ComputerDrag: computer.Drag,
	tools.ComputerScroll: computer.Scroll, tools.ComputerKey: computer.Key, tools.ComputerType: computer.Type, tools.ComputerText: computer.Text,
	tools.ComputerWait: computer.Wait, tools.ComputerScreenshot: computer.Screenshot, tools.ComputerLaunch: computer.Launch, tools.ComputerStop: computer.Stop,
}

// BoxTools runs the actions which live in the box instead of the engine.
func BoxTools(socket string) Local {
	agent := computer.NewAgent(socket)
	return func(ctx context.Context, handler tools.BoxHandler, raw []byte) (Result, *contract.Error) {
		fail := func(err error) (Result, *contract.Error) {
			return Result{}, &contract.Error{Code: contract.ErrInvalid, Message: err.Error()}
		}
		if handler == tools.VisualCheck {
			var req tools.FileRequest
			_ = json.Unmarshal(raw, &req)
			data, err := visual.ReadFile(req.File)
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
			return Result{Content: []Content{{Type: "text", Text: string(output)}}, StructuredContent: output}, nil
		}
		action, ok := computerActions[handler]
		if !ok {
			return fail(fmt.Errorf("unknown box action"))
		}
		// The tool decoded the fields strictly against the action's own request; the box's request holds them all.
		var req computer.Request
		if err := json.Unmarshal(raw, &req); err != nil {
			return fail(err)
		}
		out, err := computer.Do(ctx, agent, action, req)
		if err != nil {
			return fail(err)
		}
		var parts []Content
		if out.PNG != nil {
			parts = append(parts, Content{Type: "image", Data: base64.StdEncoding.EncodeToString(out.PNG), MimeType: "image/png"})
		}
		if out.Text != "" || out.PNG == nil {
			parts = append(parts, Content{Type: "text", Text: out.Text})
		}
		return Result{Content: parts}, nil
	}
}

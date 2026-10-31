package visual

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// The fake DevTools peer exercises the complete check and screenshot path
// without launching Chromium or waiting for a page load.
func TestInspectWithFakeBrowser(t *testing.T) {
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer output.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &client{input: output, messages: make(chan message, 64), readError: make(chan error, 1)}
	go func() {
		r := bufio.NewReader(input)
		for {
			raw, err := r.ReadBytes(0)
			if err != nil {
				return
			}
			var cmd struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
				Params struct {
					Expression string `json:"expression"`
				} `json:"params"`
			}
			if json.Unmarshal(raw[:len(raw)-1], &cmd) != nil {
				return
			}
			result := `{}`
			switch cmd.Method {
			case "Target.createTarget":
				result = `{"targetId":"target"}`
			case "Target.attachToTarget":
				result = `{"sessionId":"session"}`
			case "Page.navigate":
				c.messages <- message{Method: "Page.loadEventFired", SessionID: "session"}
			case "Runtime.evaluate":
				value := "false"
				if cmd.Params.Expression == visibleContentScript {
					value = "true"
				}
				if cmd.Params.Expression == contrastScript {
					value = `{"count":0,"sample":""}`
				}
				result = `{"result":{"value":` + value + `}}`
			case "Page.captureScreenshot":
				result = `{"data":"` + base64.StdEncoding.EncodeToString([]byte("image")) + `"}`
			}
			c.messages <- message{ID: cmd.ID, Result: json.RawMessage(result)}
		}
	}()
	result, err := (&browser{c: c}).inspect(ctx, []byte("<p>Hello</p>"), "default-src 'none'", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Checks) != 5 || len(result.Widths) != 3 || string(result.Banner) != "image" || string(result.Thumb) != "image" {
		t.Fatalf("unexpected result: %+v", result)
	}
	for _, check := range result.Checks {
		if check.Status != contract.VisualCheckPass {
			t.Errorf("check: %+v", check)
		}
	}
	for i, width := range []int{360, 768, 1280} {
		if result.Widths[i].Width != width || string(result.Widths[i].Image) != "image" {
			t.Errorf("shot: %+v", result.Widths[i])
		}
	}
}

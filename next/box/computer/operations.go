package computer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type Action string

const (
	Open       Action = "open"
	Click      Action = "click"
	Type       Action = "type"
	Text       Action = "text"
	Screenshot Action = "screenshot"
)

type viewport struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Scale  float64 `json:"scale"`
}

type params struct {
	Width                 int       `json:"width,omitempty"`
	Height                int       `json:"height,omitempty"`
	DeviceScaleFactor     int       `json:"deviceScaleFactor,omitempty"`
	Mobile                *bool     `json:"mobile,omitempty"`
	URL                   string    `json:"url,omitempty"`
	Expression            string    `json:"expression,omitempty"`
	ReturnByValue         bool      `json:"returnByValue,omitempty"`
	Text                  string    `json:"text,omitempty"`
	Format                string    `json:"format,omitempty"`
	CaptureBeyondViewport bool      `json:"captureBeyondViewport,omitempty"`
	Clip                  *viewport `json:"clip,omitempty"`
}

// Execute applies one CLI operation to a connected page.
func Execute(ctx context.Context, d *DevTools, action Action, args []string, size string, full bool, out io.Writer) error {
	var err error
	call := func(method string, params json.RawMessage) (json.RawMessage, error) {
		return d.Call(ctx, method, params)
	}
	marshal := func(v params) json.RawMessage { data, _ := json.Marshal(v); return data }
	switch action {
	case Open:
		if size != "" {
			parts := strings.Split(size, "x")
			if len(parts) != 2 {
				return fmt.Errorf("size must be WIDTHxHEIGHT")
			}
			w, e1 := strconv.Atoi(parts[0])
			h, e2 := strconv.Atoi(parts[1])
			if e1 != nil || e2 != nil || w <= 0 || h <= 0 {
				return fmt.Errorf("size must be WIDTHxHEIGHT")
			}
			_, err = call("Emulation.setDeviceMetricsOverride", marshal(params{Width: w, Height: h, DeviceScaleFactor: 1, Mobile: new(bool)}))
			if err != nil {
				return err
			}
		}
		if _, err = call("Page.enable", nil); err != nil {
			return err
		}
		data, err := call("Page.navigate", marshal(params{URL: args[0]}))
		if err != nil {
			return err
		}
		var navigation struct {
			ErrorText string `json:"errorText"`
			LoaderID  string `json:"loaderId"`
		}
		if err = json.Unmarshal(data, &navigation); err != nil {
			return err
		}
		if navigation.ErrorText != "" {
			return fmt.Errorf("navigate: %s", navigation.ErrorText)
		}
		if navigation.LoaderID == "" {
			return nil // Same-document navigation does not fire a load event.
		}
		return d.WaitLoad(ctx)
	case Click, Type, Text:
		selector := "body"
		if len(args) > 0 {
			selector = args[0]
		}
		quoted, _ := json.Marshal(selector)
		expression := "(() => { const e = document.querySelector(" + string(quoted) + "); if (!e) throw new Error('selector not found'); "
		switch action {
		case Click:
			expression += "e.click();"
		case Type:
			expression += "e.focus();"
		case Text:
			expression += "return e.innerText;"
		}
		expression += " })()"
		data, err := call("Runtime.evaluate", marshal(params{Expression: expression, ReturnByValue: true}))
		if err != nil {
			return err
		}
		var result struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
			Exception *struct {
				Text string `json:"text"`
			} `json:"exceptionDetails"`
		}
		if err = json.Unmarshal(data, &result); err != nil {
			return err
		}
		if result.Exception != nil {
			return fmt.Errorf("evaluate: %s", result.Exception.Text)
		}
		if action == Text {
			_, err = fmt.Fprintln(out, result.Result.Value)
			return err
		}
		if action == Type {
			_, err = call("Input.insertText", marshal(params{Text: args[1]}))
			return err
		}
		return nil
	case Screenshot:
		options := params{Format: "png", CaptureBeyondViewport: full}
		if full {
			metrics, err := call("Page.getLayoutMetrics", nil)
			if err != nil {
				return err
			}
			var layout struct {
				Content viewport `json:"cssContentSize"`
			}
			if err := json.Unmarshal(metrics, &layout); err != nil {
				return err
			}
			layout.Content.Scale = 1
			options.Clip = &layout.Content
		}
		data, err := call("Page.captureScreenshot", marshal(options))
		if err != nil {
			return err
		}
		var result struct {
			Data string `json:"data"`
		}
		if err = json.Unmarshal(data, &result); err != nil {
			return err
		}
		png, err := base64.StdEncoding.DecodeString(result.Data)
		if err != nil {
			return err
		}
		return os.WriteFile(args[0], png, 0644)
	}
	return nil
}

package computer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// The page actions drive Chromium's current page through DevTools: open, text, and a selector's click or type.

type viewport struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Scale  float64 `json:"scale"`
}

type params struct {
	URL                   string    `json:"url,omitempty"`
	Expression            string    `json:"expression,omitempty"`
	ReturnByValue         bool      `json:"returnByValue,omitempty"`
	Text                  string    `json:"text,omitempty"`
	Format                string    `json:"format,omitempty"`
	CaptureBeyondViewport bool      `json:"captureBeyondViewport,omitempty"`
	Clip                  *viewport `json:"clip,omitempty"`
}

func (d *DevTools) call(ctx context.Context, method string, p params) (json.RawMessage, error) {
	data, _ := json.Marshal(p)
	return d.Call(ctx, method, data)
}

// navigate opens url in the page and waits for its load, and returns the page's title.
func navigate(ctx context.Context, d *DevTools, url string) (string, error) {
	if _, err := d.Call(ctx, "Page.enable", nil); err != nil {
		return "", err
	}
	data, err := d.call(ctx, "Page.navigate", params{URL: url})
	if err != nil {
		return "", err
	}
	var navigation struct {
		ErrorText string `json:"errorText"`
		LoaderID  string `json:"loaderId"`
	}
	if err = json.Unmarshal(data, &navigation); err != nil {
		return "", err
	}
	if navigation.ErrorText != "" {
		return "", fmt.Errorf("navigate: %s", navigation.ErrorText)
	}
	// A same-document navigation fires no load event.
	if navigation.LoaderID != "" {
		if err := d.WaitLoad(ctx); err != nil {
			return "", err
		}
	}
	var title string
	err = evaluate(ctx, d, "document.title", &title)
	return title, err
}

// evaluate runs expression in the page and decodes its value into out.
func evaluate(ctx context.Context, d *DevTools, expression string, out any) error {
	data, err := d.call(ctx, "Runtime.evaluate", params{Expression: expression, ReturnByValue: true})
	if err != nil {
		return err
	}
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return err
	}
	if e := result.Exception; e != nil {
		if e.Exception != nil && e.Exception.Description != "" {
			return fmt.Errorf("page: %s", e.Exception.Description)
		}
		return fmt.Errorf("page: %s", e.Text)
	}
	if out == nil || len(result.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(result.Result.Value, out)
}

// element is the expression that finds selector or throws.
func element(selector string) string {
	quoted, _ := json.Marshal(selector)
	return "document.querySelector(" + string(quoted) + ") ?? (() => { throw new Error('no element matches the selector') })()"
}

// pageText returns the text of selector, or of the whole page.
func pageText(ctx context.Context, d *DevTools, selector string) (string, error) {
	if selector == "" {
		selector = "body"
	}
	var text string
	err := evaluate(ctx, d, "("+element(selector)+").innerText", &text)
	return text, err
}

// pageInsert focuses selector and inserts text there, as a paste does.
func pageInsert(ctx context.Context, d *DevTools, selector, text string) error {
	if err := evaluate(ctx, d, "("+element(selector)+").focus()", nil); err != nil {
		return err
	}
	_, err := d.call(ctx, "Input.insertText", params{Text: text})
	return err
}

// elementPoint scrolls selector into view and returns its center on the desktop, where a real click lands on it. The
// page's top left on the desktop is the window's, below Chromium's own tab strip and toolbar.
func elementPoint(ctx context.Context, d *DevTools, selector string) (int, int, error) {
	var at []float64
	err := evaluate(ctx, d, `(() => { const e = `+element(selector)+`; e.scrollIntoView({ block: "center", inline: "center" });
		const r = e.getBoundingClientRect(), z = devicePixelRatio;
		return [screenX + ((outerWidth - innerWidth) / 2 + r.left + r.width / 2) * z, screenY + (outerHeight - innerHeight + r.top + r.height / 2) * z]; })()`, &at)
	if err != nil {
		return 0, 0, err
	}
	if len(at) != 2 {
		return 0, 0, fmt.Errorf("the element has no place on the page")
	}
	return int(at[0]), int(at[1]), nil
}

// pageScreenshot captures the whole page, beyond the window, as a PNG.
func pageScreenshot(ctx context.Context, d *DevTools) ([]byte, bool, error) {
	metrics, err := d.Call(ctx, "Page.getLayoutMetrics", nil)
	if err != nil {
		return nil, false, err
	}
	var layout struct {
		Content viewport `json:"cssContentSize"`
	}
	if err := json.Unmarshal(metrics, &layout); err != nil {
		return nil, false, err
	}
	clipped := layout.Content.Height > 16384
	layout.Content.Height = min(layout.Content.Height, 16384)
	layout.Content.Scale = 1
	data, err := d.call(ctx, "Page.captureScreenshot", params{Format: "png", CaptureBeyondViewport: true, Clip: &layout.Content})
	if err != nil {
		return nil, false, err
	}
	var result struct {
		Data string `json:"data"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, false, err
	}
	png, err := base64.StdEncoding.DecodeString(result.Data)
	return png, clipped, err
}

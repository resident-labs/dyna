package computer

import (
	"context"
	"encoding/json"
	"log"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
)

// Input sends the owner's events to the page. It keeps one DevTools connection between batches and
// the mouse buttons held down, so a drag moves with its button.
type Input struct {
	Base     string
	queue    chan contract.ComputerInputBatch
	overflow atomic.Bool // a batch was dropped, so a release may be missing
	d        *DevTools
	buttons  int
	lastX    float64 // CSS pixels of the last mouse event
	lastY    float64
	keys     map[string]keyParams // keys held down, as the key-up that releases each
}

func NewInput(base string) *Input {
	return &Input{Base: base, queue: make(chan contract.ComputerInputBatch, 64), keys: map[string]keyParams{}}
}

// Enqueue never blocks, so a stuck browser cannot hold the box channel. It reports false when the
// queue is full and the batch is dropped. A drop may lose a key or button release, so the worker
// releases everything held before its next batch.
func (in *Input) Enqueue(batch contract.ComputerInputBatch) bool {
	select {
	case in.queue <- batch:
		return true
	default:
		in.overflow.Store(true)
		return false
	}
}

// Run sends queued batches in order until ctx ends. It gives each batch 2 seconds.
func (in *Input) Run(ctx context.Context) {
	for {
		select {
		case batch := <-in.queue:
			sendCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			batches, release := in.collect(batch)
			if err := in.Send(sendCtx, release, batches...); err != nil {
				log.Printf("computer input: %v", err)
			}
			cancel()
		case <-ctx.Done():
			return
		}
	}
}

type keyParams struct {
	Type       string `json:"type"`
	Modifiers  int    `json:"modifiers,omitempty"`
	Key        string `json:"key"`
	Code       string `json:"code,omitempty"`
	Text       string `json:"text,omitempty"`
	VirtualKey int    `json:"windowsVirtualKeyCode,omitempty"`
}

type mouseParams struct {
	Type       string  `json:"type"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Button     string  `json:"button"`
	Buttons    int     `json:"buttons"`
	ClickCount int     `json:"clickCount,omitempty"`
	DeltaX     float64 `json:"deltaX"`
	DeltaY     float64 `json:"deltaY"`
	Modifiers  int     `json:"modifiers,omitempty"`
}

var virtualKeys = map[string]int{
	"Backspace": 8, "Tab": 9, "Enter": 13, "Shift": 16, "Control": 17, "Alt": 18, "Escape": 27,
	"PageUp": 33, "PageDown": 34, "End": 35, "Home": 36, "ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39,
	"ArrowDown": 40, "Delete": 46,
}

// collect adds to first every batch already queued when a drop happened, so the release that follows
// comes after all of them. The queue is only drained from one worker, so len is exact.
func (in *Input) collect(first contract.ComputerInputBatch) ([]contract.ComputerInputBatch, bool) {
	batches := []contract.ComputerInputBatch{first}
	if !in.overflow.Swap(false) {
		return batches, false
	}
	for range len(in.queue) {
		batches = append(batches, <-in.queue)
	}
	return batches, true
}

// Send dispatches batches in order, then lifts every held key and button if release is set. It drops the
// connection on an error so the next batch reconnects. If a release was due and the send failed, it
// retries the release at once on a fresh connection, and asks the next batch to retry if that fails too.
func (in *Input) Send(ctx context.Context, release bool, batches ...contract.ComputerInputBatch) error {
	err := in.dispatch(ctx, release, batches)
	if err != nil && release {
		if in.dispatch(ctx, true, nil) != nil {
			in.overflow.Store(true)
		}
	}
	return err
}

func (in *Input) dispatch(ctx context.Context, release bool, batches []contract.ComputerInputBatch) error {
	if in.d == nil {
		d, err := Connect(ctx, in.Base)
		if err != nil {
			return err
		}
		in.d = d
	}
	var err error
	for _, batch := range batches {
		if err = in.send(ctx, in.d, batch); err != nil {
			break
		}
	}
	if err == nil && release {
		err = in.releaseAll(ctx, in.d)
	}
	if err != nil {
		in.d.Close()
		in.d = nil
	}
	return err
}

// releaseAll lifts every key and mouse button the owner holds, at the last mouse position.
func (in *Input) releaseAll(ctx context.Context, d *DevTools) error {
	for key, p := range in.keys {
		raw, _ := json.Marshal(p)
		if _, err := d.Call(ctx, "Input.dispatchKeyEvent", raw); err != nil {
			return err
		}
		delete(in.keys, key)
	}
	for button, bit := range map[contract.MouseButton]int{contract.ButtonLeft: 1, contract.ButtonRight: 2, contract.ButtonMiddle: 4} {
		if in.buttons&bit == 0 {
			continue
		}
		raw, _ := json.Marshal(mouseParams{Type: "mouseReleased", X: in.lastX, Y: in.lastY, Button: string(button), Buttons: in.buttons &^ bit, ClickCount: 1})
		if _, err := d.Call(ctx, "Input.dispatchMouseEvent", raw); err != nil {
			return err
		}
		in.buttons &^= bit
	}
	return nil
}

func (in *Input) send(ctx context.Context, d *DevTools, batch contract.ComputerInputBatch) error {
	var viewW, viewH float64
	for _, e := range batch.Events {
		var method string
		var params any
		switch e.Kind {
		case contract.InputKeyDown, contract.InputKeyUp:
			p := keyParams{Type: "keyUp", Modifiers: int(e.Modifiers), Key: e.Key, Code: e.Code, VirtualKey: virtualKeys[e.Key]}
			if r, size := utf8.DecodeRuneInString(e.Key); size == len(e.Key) && r < 128 {
				if p.VirtualKey = int(r); r >= 'a' && r <= 'z' {
					p.VirtualKey = int(r - 'a' + 'A')
				}
			}
			if e.Kind == contract.InputKeyDown {
				p.Type = "rawKeyDown"
				if e.Key == "Enter" {
					p.Type, p.Text = "keyDown", "\r"
				} else if utf8.RuneCountInString(e.Key) == 1 && e.Modifiers&(contract.ModCtrl|contract.ModAlt|contract.ModMeta) == 0 {
					p.Type, p.Text = "keyDown", e.Key
				}
			}
			method, params = "Input.dispatchKeyEvent", p
			if e.Kind == contract.InputKeyDown {
				p.Type, p.Text = "keyUp", ""
				in.keys[e.Key] = p
			} else {
				delete(in.keys, e.Key)
			}
		case contract.InputChar:
			method, params = "Input.insertText", struct {
				Text string `json:"text"`
			}{e.Text}
		default:
			if viewW == 0 {
				var m struct {
					Viewport struct {
						Width  float64 `json:"clientWidth"`
						Height float64 `json:"clientHeight"`
					} `json:"cssVisualViewport"`
				}
				result, err := d.Call(ctx, "Page.getLayoutMetrics", nil)
				if err == nil {
					err = json.Unmarshal(result, &m)
				}
				if err != nil {
					return err
				}
				viewW, viewH = m.Viewport.Width, m.Viewport.Height
			}
			x, y := cssPoint(batch.Frame, viewW, viewH, e.X, e.Y)
			in.lastX, in.lastY = x, y
			p := mouseParams{Type: "mouseMoved", X: x, Y: y, Button: "none", Buttons: in.buttons, Modifiers: int(e.Modifiers)}
			switch e.Kind {
			case contract.InputMouseDown, contract.InputMouseUp:
				bit := map[contract.MouseButton]int{contract.ButtonLeft: 1, contract.ButtonRight: 2, contract.ButtonMiddle: 4}[e.Button]
				p.Button, p.ClickCount = string(e.Button), 1
				if e.Kind == contract.InputMouseDown {
					p.Type, in.buttons = "mousePressed", in.buttons|bit
				} else {
					p.Type, in.buttons = "mouseReleased", in.buttons&^bit
				}
				p.Buttons = in.buttons
			case contract.InputWheel:
				p.Type = "mouseWheel"
				p.DeltaX, p.DeltaY = cssPoint(batch.Frame, viewW, viewH, e.DeltaX, e.DeltaY)
			}
			method, params = "Input.dispatchMouseEvent", p
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		if _, err = d.Call(ctx, method, raw); err != nil {
			return err
		}
	}
	return nil
}

// cssPoint scales a position in frame pixels to the page's CSS pixels. The screencast JPEG shows the
// whole CSS viewport, so each axis scales by viewport size over frame size.
func cssPoint(frame contract.FrameSize, viewW, viewH, x, y float64) (float64, float64) {
	return x * viewW / float64(frame.Width), y * viewH / float64(frame.Height)
}

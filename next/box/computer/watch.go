package computer

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"time"

	"github.com/coder/websocket"
)

// Watch sends JPEG frames and acknowledges each only after it reaches the stream.
func Watch(ctx context.Context, out io.Writer) error {
	return watch(ctx, URL, out, func(ctx context.Context) error {
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

func watch(ctx context.Context, base string, out io.Writer, pace func(context.Context) error) error {
	d, err := Connect(ctx, base)
	if err != nil {
		return err
	}
	defer d.Close()
	if _, err = d.Call(ctx, "Page.enable", nil); err != nil {
		return err
	}
	if _, err = d.Call(ctx, "Page.startScreencast", json.RawMessage(`{"format":"jpeg","quality":70,"maxWidth":1280,"everyNthFrame":1}`)); err != nil {
		return err
	}
	result, err := d.Call(ctx, "Page.captureScreenshot", json.RawMessage(`{"format":"jpeg","quality":70}`))
	if err != nil {
		return err
	}
	// Ignore delayed frames captured before the screenshot completed.
	lastFrameTime := float64(time.Now().UnixNano()) / 1e9
	var shot struct {
		Data string `json:"data"`
	}
	if err = json.Unmarshal(result, &shot); err != nil {
		return err
	}
	// Frames received before the screenshot response predate the screenshot.
	// Acknowledge them without showing them, so they cannot replace it.
	for _, m := range d.pending {
		var frame struct {
			SessionID int `json:"sessionId"`
		}
		if err = json.Unmarshal(m.Params, &frame); err != nil {
			return err
		}
		if err = ackFrame(ctx, d, frame.SessionID); err != nil {
			return err
		}
	}
	d.pending = nil
	if err = writeFrame(out, shot.Data); err != nil {
		return err
	}
	for {
		var m message
		if len(d.pending) > 0 {
			m = d.pending[0]
			d.pending = d.pending[1:]
		} else {
			_, data, err := d.conn.Read(ctx)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &m); err != nil {
				return err
			}
		}
		if m.Method != "Page.screencastFrame" {
			continue
		}
		var frame struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
			Metadata  struct {
				Timestamp float64 `json:"timestamp"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(m.Params, &frame); err != nil {
			return err
		}
		if frame.Metadata.Timestamp < lastFrameTime {
			if err = ackFrame(ctx, d, frame.SessionID); err != nil {
				return err
			}
			continue
		}
		lastFrameTime = frame.Metadata.Timestamp
		if err = writeFrame(out, frame.Data); err != nil {
			return err
		}
		// Pace acknowledgements so busy pages do not stream at display refresh rate.
		if err = pace(ctx); err != nil {
			return err
		}
		if err = ackFrame(ctx, d, frame.SessionID); err != nil {
			return err
		}
	}
}

func writeFrame(out io.Writer, data string) error {
	jpeg, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	if err = binary.Write(out, binary.BigEndian, uint32(len(jpeg))); err != nil {
		return err
	}
	_, err = out.Write(jpeg)
	return err
}

func ackFrame(ctx context.Context, d *DevTools, sessionID int) error {
	d.id++
	params, _ := json.Marshal(struct {
		SessionID int `json:"sessionId"`
	}{sessionID})
	ack, _ := json.Marshal(message{ID: d.id, Method: "Page.screencastFrameAck", Params: params})
	return d.conn.Write(ctx, websocket.MessageText, ack)
}

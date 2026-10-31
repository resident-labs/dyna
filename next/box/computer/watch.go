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
	return watch(ctx, URL, out)
}

func watch(ctx context.Context, base string, out io.Writer) error {
	d, err := Connect(ctx, base)
	if err != nil {
		return err
	}
	defer d.Close()
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		d.id++
		data, _ := json.Marshal(message{ID: d.id, Method: "Page.stopScreencast"})
		_ = d.conn.Write(stop, websocket.MessageText, data)
	}()
	if _, err = d.Call(ctx, "Page.enable", nil); err != nil {
		return err
	}
	if _, err = d.Call(ctx, "Page.startScreencast", json.RawMessage(`{"format":"jpeg","quality":70,"maxWidth":1280,"everyNthFrame":6}`)); err != nil {
		return err
	}
	var next time.Time
	for {
		_, data, err := d.conn.Read(ctx)
		if err != nil {
			return err
		}
		var m message
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		if m.Method != "Page.screencastFrame" {
			continue
		}
		var frame struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
		}
		if err := json.Unmarshal(m.Params, &frame); err != nil {
			return err
		}
		jpeg, err := base64.StdEncoding.DecodeString(frame.Data)
		if err != nil {
			return err
		}
		if delay := time.Until(next); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
		if err = binary.Write(out, binary.BigEndian, uint32(len(jpeg))); err != nil {
			return err
		}
		if _, err = out.Write(jpeg); err != nil {
			return err
		}
		next = time.Now().Add(100 * time.Millisecond)
		d.id++
		params, _ := json.Marshal(struct {
			SessionID int `json:"sessionId"`
		}{frame.SessionID})
		ack, _ := json.Marshal(message{ID: d.id, Method: "Page.screencastFrameAck", Params: params})
		if err = d.conn.Write(ctx, websocket.MessageText, ack); err != nil {
			return err
		}
	}
}

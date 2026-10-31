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
		if err = binary.Write(out, binary.BigEndian, uint32(len(jpeg))); err != nil {
			return err
		}
		if _, err = out.Write(jpeg); err != nil {
			return err
		}
		// Pace acknowledgements so busy pages do not stream at display refresh rate.
		if err = pace(ctx); err != nil {
			return err
		}
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

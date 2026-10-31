package computer

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"

	"github.com/coder/websocket"
)

// Watch sends JPEG frames and acknowledges each only after it reaches the stream.
func Watch(ctx context.Context, out io.Writer) error {
	return watch(ctx, URL, out)
}

func watch(ctx context.Context, base string, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d, err := Connect(ctx, base)
	if err != nil {
		return err
	}
	defer d.Close()
	if _, err = d.Call(ctx, "Page.enable", nil); err != nil {
		return err
	}
	if _, err = d.Call(ctx, "Page.startScreencast", json.RawMessage(`{"format":"jpeg","quality":70,"maxWidth":1280,"everyNthFrame":6}`)); err != nil {
		return err
	}
	type batch struct {
		jpeg []byte
		ids  []int
		err  error
	}
	frames := make(chan batch, 1)
	go func() {
		defer close(frames)
		for {
			_, data, err := d.conn.Read(ctx)
			if err != nil {
				select {
				case frames <- batch{err: err}:
				case <-ctx.Done():
				}
				return
			}
			var m message
			if err = json.Unmarshal(data, &m); err != nil {
				select {
				case frames <- batch{err: err}:
				case <-ctx.Done():
				}
				return
			}
			if m.Method != "Page.screencastFrame" {
				continue
			}
			var frame struct {
				Data      string `json:"data"`
				SessionID int    `json:"sessionId"`
			}
			if err = json.Unmarshal(m.Params, &frame); err != nil {
				select {
				case frames <- batch{err: err}:
				case <-ctx.Done():
				}
				return
			}
			jpeg, err := base64.StdEncoding.DecodeString(frame.Data)
			if err != nil {
				select {
				case frames <- batch{err: err}:
				case <-ctx.Done():
				}
				return
			}
			current := batch{jpeg: jpeg, ids: []int{frame.SessionID}}
			select {
			case previous := <-frames:
				current.ids = append(previous.ids, current.ids...)
			default:
			}
			select {
			case frames <- current:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		var frame batch
		select {
		case frame = <-frames:
		case <-ctx.Done():
			return ctx.Err()
		}
		if frame.err != nil {
			return frame.err
		}
		if len(frame.jpeg) == 0 {
			return io.EOF
		}
		if err = binary.Write(out, binary.BigEndian, uint32(len(frame.jpeg))); err != nil {
			return err
		}
		if _, err = out.Write(frame.jpeg); err != nil {
			return err
		}
		for _, id := range frame.ids {
			d.id++
			params, _ := json.Marshal(struct {
				SessionID int `json:"sessionId"`
			}{id})
			ack, _ := json.Marshal(message{ID: d.id, Method: "Page.screencastFrameAck", Params: params})
			if err = d.conn.Write(ctx, websocket.MessageText, ack); err != nil {
				return err
			}
		}
	}
}

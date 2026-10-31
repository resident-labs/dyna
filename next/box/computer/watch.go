package computer

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/coder/websocket"
)

// Watch sends JPEG frames and paces screencast notifications.
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
	return watchWithTimer(ctx, base, out, pace, func() (<-chan time.Time, func()) {
		timer := time.NewTimer(time.Second)
		return timer.C, func() { timer.Stop() }
	})
}

func watchWithTimer(ctx context.Context, base string, out io.Writer, pace func(context.Context) error, newQuiet func() (<-chan time.Time, func())) error {
	ctx, cancel := context.WithCancel(ctx)
	d, err := Connect(ctx, base)
	if err != nil {
		cancel()
		return err
	}
	defer d.Close()
	defer cancel()
	if _, err = d.Call(ctx, "Page.enable", nil); err != nil {
		return err
	}
	if _, err = d.Call(ctx, "Page.startScreencast", json.RawMessage(`{"format":"jpeg","quality":70,"maxWidth":1280,"everyNthFrame":1}`)); err != nil {
		return err
	}
	type incoming struct {
		message message
		err     error
	}
	incomingMessages := make(chan incoming)
	go func() {
		for {
			var m message
			var err error
			if len(d.pending) > 0 {
				m = d.pending[0]
				d.pending = d.pending[1:]
			} else {
				var data []byte
				_, data, err = d.conn.Read(ctx)
				if err == nil {
					err = json.Unmarshal(data, &m)
				}
			}
			select {
			case incomingMessages <- incoming{m, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var captureID int
	invalidCapture := false
	capture := func() error {
		d.id++
		captureID = d.id
		invalidCapture = false
		request, _ := json.Marshal(message{ID: captureID, Method: "Page.captureScreenshot", Params: json.RawMessage(`{"format":"jpeg","quality":70}`)})
		return d.conn.Write(ctx, websocket.MessageText, request)
	}
	if err = capture(); err != nil {
		return err
	}
	var quiet <-chan time.Time
	stopQuiet := func() {}
	defer func() { stopQuiet() }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-quiet:
			quiet = nil
			if captureID == 0 {
				if err = capture(); err != nil {
					return err
				}
			}
		case item := <-incomingMessages:
			if item.err != nil {
				return item.err
			}
			m := item.message
			if captureID != 0 && m.ID == captureID {
				captureID = 0
				if m.Error != nil {
					return fmt.Errorf("DevTools Page.captureScreenshot: %s", m.Error.Message)
				}
				if !invalidCapture {
					var shot struct {
						Data string `json:"data"`
					}
					if err = json.Unmarshal(m.Result, &shot); err != nil {
						return err
					}
					if err = writeFrame(out, shot.Data); err != nil {
						return err
					}
				} else if quiet == nil {
					quiet, stopQuiet = newQuiet()
				}
				continue
			}
			if m.Method != "Page.screencastFrame" {
				continue
			}
			var frame struct {
				Data      string `json:"data"`
				SessionID int    `json:"sessionId"`
			}
			if err = json.Unmarshal(m.Params, &frame); err != nil {
				return err
			}
			if captureID != 0 {
				invalidCapture = true
			}
			if err = writeFrame(out, frame.Data); err != nil {
				return err
			}
			stopQuiet()
			quiet, stopQuiet = newQuiet()
			if err = pace(ctx); err != nil {
				return err
			}
			if err = ackFrame(ctx, d, frame.SessionID); err != nil {
				return err
			}
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

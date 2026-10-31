package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Channel reads typed messages and reconnects after a broken socket.
func (c *Client) Channel(ctx context.Context, handle func(contract.BoxMessage) error, connected func(bool)) error {
	delay := 500 * time.Millisecond
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://dyna/v1/box/channel", nil)
		if err != nil {
			return err
		}
		resp, err := c.http.Do(req)
		if err == nil {
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				return fmt.Errorf("box channel: %s", resp.Status)
			}
			delay = 500 * time.Millisecond
			connected(true)
			decoder := json.NewDecoder(resp.Body)
			for {
				var msg contract.BoxMessage
				if err = decoder.Decode(&msg); errors.Is(err, contract.ErrUnknownBoxMessage) {
					log.Printf("box channel: %v; skipped", err)
					continue
				} else if err != nil {
					var netErr net.Error
					if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.As(err, &netErr) {
						resp.Body.Close()
						return err
					}
					break
				}
				if err = handle(msg); err != nil {
					if errors.Is(err, errTurnSettling) {
						break
					}
					resp.Body.Close()
					return err
				}
			}
			resp.Body.Close()
			connected(false)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
		delay *= 2
		if delay > 10*time.Second {
			delay = 10 * time.Second
		}
	}
}

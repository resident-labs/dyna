package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// runningSum hashes the binary this process runs, which stays readable after a deploy replaces the file. The engine compares it
// with its own. A container's environment could not carry it: a stopped box starts again with the environment it was made with.
func runningSum() (string, error) {
	file, err := os.Open("/proc/self/exe")
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Channel reads typed messages and reconnects after a broken socket. Each connection reports binary, which is how an engine
// that restarted learns it, since a running box does not say hello again.
func (c *Client) Channel(ctx context.Context, binary string, handle func(contract.BoxMessage) error, connected func(bool)) error {
	delay := 500 * time.Millisecond
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://dyna/v1/box/channel?binary="+url.QueryEscape(binary), nil)
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

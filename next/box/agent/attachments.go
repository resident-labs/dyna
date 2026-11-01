package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// fetchStall is how long a file download may go without a byte before it starts over. A relayed host can be slow, and a
// stalled one must not hold a turn for good. It is a variable so that tests can shorten it.
var fetchStall = time.Minute

// Attach stores an input's files at the paths its frame names. A file it cannot get stays out of the box, and the frame says
// so, because the sender's words still matter. It fails only when ctx ends, which leaves the input for the next turn.
func (c *Client) Attach(ctx context.Context, input contract.TurnInput) (contract.TurnInput, error) {
	for _, file := range input.Files {
		err := c.storeFile(ctx, file)
		if ctx.Err() != nil {
			return input, ctx.Err()
		}
		if err != nil {
			log.Printf("attachment %s: %v", file.Path, err)
			frame, text, _ := strings.Cut(input.Text, "\n")
			input.Text = fmt.Sprintf("%s %s could not be fetched, so it is not in the box.\n%s", frame, file.Path, text)
		}
	}
	return input, nil
}

// storeFile fetches one file unless a replay already stored it. It retries a broken connection, as an engine restart or a
// lost relay breaks one, until no attempt has got further for as long as the model route waits for the socket.
func (c *Client) storeFile(ctx context.Context, file contract.TurnFile) error {
	if info, err := os.Stat(file.Path); err == nil && info.Size() == file.Bytes {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file.Path), 0755); err != nil {
		return err
	}
	var rejected apiError
	var furthest int64
	moved := time.Now()
	for delay := 500 * time.Millisecond; ; delay = min(2*delay, 10*time.Second) {
		received, err := c.fetchFile(ctx, file)
		if err == nil || errors.As(err, &rejected) || ctx.Err() != nil {
			return err
		}
		if received > furthest {
			furthest, moved = received, time.Now()
		} else if time.Since(moved) > socketWait {
			return err
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// fetchFile downloads one file beside its path and renames it into place, so a path that exists is whole.
func (c *Client) fetchFile(ctx context.Context, file contract.TurnFile) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(fetchStall, cancel)
	defer stall.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://dyna/v1/files/"+file.SHA256, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, apiError{status: "file download: " + resp.Status}
	}
	temp, err := os.CreateTemp(filepath.Dir(file.Path), ".fetch-")
	if err != nil {
		return 0, apiError{status: "file download: " + err.Error()}
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	hash := sha256.New()
	received, err := io.Copy(io.MultiWriter(temp, hash), readerFunc(func(p []byte) (int, error) {
		stall.Reset(fetchStall)
		return resp.Body.Read(p)
	}))
	if err != nil {
		return received, err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != file.SHA256 {
		return received, apiError{status: "file download: got " + got}
	}
	if err := temp.Close(); err != nil {
		return received, apiError{status: "file download: " + err.Error()}
	}
	if err := os.Rename(temp.Name(), file.Path); err != nil {
		return received, apiError{status: "file download: " + err.Error()}
	}
	return received, nil
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

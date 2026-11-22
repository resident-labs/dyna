package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/resident-labs/dyna/box/browser"
	"github.com/resident-labs/dyna/box/computer"
	"github.com/resident-labs/dyna/contract"
)

// Checkout acquires the room's shared browser profile lease from the engine and unpacks it into dir.
// When the room has no shared browser profile, it returns an empty Lease and nil error.
func (c *Client) Checkout(ctx context.Context, dir string) (computer.Lease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dyna/v1/browser/checkout", nil)
	if err != nil {
		return computer.Lease{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return computer.Lease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return computer.Lease{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		problem := contract.ReadError(resp.Body)
		if problem.Message != "" {
			return computer.Lease{}, errors.New(problem.Message)
		}
		return computer.Lease{}, fmt.Errorf("browser checkout: %s", resp.Status)
	}
	profile := resp.Header.Get("X-Dyna-Browser-Profile")
	gen, err := strconv.ParseInt(resp.Header.Get("X-Dyna-Browser-Generation"), 10, 64)
	if profile == "" || err != nil || gen <= 0 {
		return computer.Lease{}, fmt.Errorf("browser checkout: invalid response headers")
	}
	lease := computer.Lease{Profile: profile, Generation: gen}
	if err := browser.UnpackProfile(resp.Body, dir); err != nil {
		_ = os.RemoveAll(dir)
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		_ = c.Checkin(releaseCtx, lease, "", true)
		cancel()
		return computer.Lease{}, fmt.Errorf("unpack shared browser profile %s: %w", profile, err)
	}
	return lease, nil
}

// Checkin packs dir and streams it back to the engine, releasing the shared browser profile lease.
// When discard is true, it releases the lease without uploading dir.
func (c *Client) Checkin(ctx context.Context, lease computer.Lease, dir string, discard bool) error {
	q := url.Values{}
	q.Set("profile", lease.Profile)
	q.Set("generation", strconv.FormatInt(lease.Generation, 10))
	if discard {
		q.Set("discard", "1")
	}
	endpoint := "http://dyna/v1/browser/checkin?" + q.Encode()
	var body io.Reader
	var packErr chan error
	if !discard {
		pr, pw := io.Pipe()
		body = pr
		packErr = make(chan error, 1)
		go func() {
			err := browser.PackProfile(dir, pw)
			_ = pw.CloseWithError(err)
			packErr <- err
		}()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		if body != nil {
			_ = body.(io.Closer).Close()
			<-packErr
		}
		return err
	}
	if !discard {
		req.Header.Set("Content-Type", "application/x-tar")
	}
	resp, err := c.http.Do(req)
	if body != nil {
		_ = body.(io.Closer).Close()
	}
	var perr error
	if packErr != nil {
		perr = <-packErr
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if perr != nil {
		return fmt.Errorf("pack shared browser profile %s: %w", lease.Profile, perr)
	}
	if resp.StatusCode == http.StatusConflict {
		return computer.ErrStaleLease
	}
	if resp.StatusCode != http.StatusOK {
		problem := contract.ReadError(resp.Body)
		if problem.Message != "" {
			return errors.New(problem.Message)
		}
		return fmt.Errorf("browser checkin: %s", resp.Status)
	}
	return nil
}

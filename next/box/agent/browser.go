package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/resident-labs/dyna/box/browser"
	"github.com/resident-labs/dyna/contract"
)

// PullCookies fetches the room's shared browser cookie jar changes since version since
// (or the full jar when since == 0). It returns active=false when the room has no profile.
func (c *Client) PullCookies(ctx context.Context, since int64) (browser.SyncResponse, bool, error) {
	endpoint := "http://dyna/v1/browser/sync"
	if since > 0 {
		endpoint += "?since=" + strconv.FormatInt(since, 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return browser.SyncResponse{}, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return browser.SyncResponse{}, false, err
	}
	defer resp.Body.Close()
	return decodeBrowserSyncResponse("pull", resp)
}

// SyncCookies pushes local cookie changes and fetches remote updates since req.BaseVersion.
// It returns active=false when the room has no shared browser profile.
func (c *Client) SyncCookies(ctx context.Context, syncReq browser.SyncRequest) (browser.SyncResponse, bool, error) {
	body, err := json.Marshal(syncReq)
	if err != nil {
		return browser.SyncResponse{}, false, errors.New("encode cookie sync request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dyna/v1/browser/sync", bytes.NewReader(body))
	if err != nil {
		return browser.SyncResponse{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return browser.SyncResponse{}, false, err
	}
	defer resp.Body.Close()
	return decodeBrowserSyncResponse("sync", resp)
}

func decodeBrowserSyncResponse(op string, resp *http.Response) (browser.SyncResponse, bool, error) {
	if resp.StatusCode == http.StatusNoContent {
		return browser.SyncResponse{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		problem := contract.ReadError(resp.Body)
		if problem.Message != "" {
			return browser.SyncResponse{}, false, errors.New(problem.Message)
		}
		return browser.SyncResponse{}, false, fmt.Errorf("browser %s: %s", op, resp.Status)
	}
	var out browser.SyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return browser.SyncResponse{}, false, fmt.Errorf("decode browser %s response", op)
	}
	return out, true, nil
}

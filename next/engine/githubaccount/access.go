package githubaccount

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// VerifyRepo refuses cached data when GitHub explicitly denies repository access.
// A network outage does not invalidate an existing mirror's offline use.
func VerifyRepo(ctx context.Context, client *http.Client, endpoint string, repo contract.RepoName, token, label string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/repos/"+string(repo), nil)
	if err != nil {
		return fmt.Errorf("%s: invalid GitHub endpoint", label)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := noRedirect.Do(req)
	if err != nil {
		return ctx.Err()
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload)
		if payload.Message == "" {
			payload.Message = http.StatusText(response.StatusCode)
		}
		return fmt.Errorf("%s: GitHub HTTP %d: %s", label, response.StatusCode, strings.ReplaceAll(payload.Message, token, "***"))
	}
	return nil
}

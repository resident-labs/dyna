package ghclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type accountProbeError struct {
	cause     string
	transient bool
}

func (e *accountProbeError) Error() string { return e.cause }

var httpStatusError = regexp.MustCompile(`HTTP ([0-9]{3})`)

// probeRepository keeps response headers for rate-limit detection. Errors never
// include gh output, since GitHub and CLI errors can contain credentials.
func probeRepository(ctx context.Context, cmd *exec.Cmd, token string, r repository, write bool) (bool, error) {
	raw, stderr, runErr := authResult(ctx, cmd, token, "api", "--hostname", r.host, "repos/"+r.name, "--include")
	if ctx.Err() != nil {
		return false, context.Cause(ctx)
	}
	code := 0
	headers := http.Header{}
	body := raw
	if resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil); err == nil {
		code, headers = resp.StatusCode, resp.Header
		// gh prints decoded body bytes after the headers. Do not apply HTTP
		// transfer decoding again (for example, chunked transfer encoding).
		if end := bytes.Index(raw, []byte("\r\n\r\n")); end >= 0 {
			body = raw[end+4:]
		} else if end := bytes.Index(raw, []byte("\n\n")); end >= 0 {
			body = raw[end+2:]
		}
	} else if match := httpStatusError.FindSubmatch(stderr); len(match) > 1 {
		code, _ = strconv.Atoi(string(match[1]))
	}
	if runErr == nil && (code == 0 || code >= 200 && code < 300) {
		if !write {
			return false, nil
		}
		var permissions struct {
			Permissions struct {
				Push *bool `json:"push"`
			} `json:"permissions"`
		}
		if json.Unmarshal(body, &permissions) != nil || permissions.Permissions.Push == nil {
			return false, &accountProbeError{"GitHub API returned invalid repository permissions; retrying", true}
		}
		return !*permissions.Permissions.Push, nil
	}
	text := strings.ToLower(string(body) + string(stderr))
	limited := code == 429 || code == 403 && (headers.Get("Retry-After") != "" || headers.Get("X-RateLimit-Remaining") == "0" || strings.Contains(text, "rate limit") || strings.Contains(text, "abuse"))
	if limited {
		return false, &accountProbeError{fmt.Sprintf("GitHub API rate limited (HTTP %d); retrying", code), true}
	}
	if code == 401 || code == 403 || code == 404 {
		return true, nil
	}
	if code == 0 {
		return false, &accountProbeError{"GitHub API probe failed (network or CLI error); retrying", true}
	}
	return false, &accountProbeError{fmt.Sprintf("GitHub API probe failed (HTTP %d); retrying", code), code >= 500}
}

func cacheAccount(key, login string) {
	accounts.Lock()
	accounts.choices[key] = choice{login, time.Now().Add(accountCacheTTL)}
	accounts.Unlock()
}

func forgetAccount(key string) {
	accounts.Lock()
	delete(accounts.choices, key)
	accounts.Unlock()
}

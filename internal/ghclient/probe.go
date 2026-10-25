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

	"dyna-agent/internal/ghquota"
)

type accountProbeError struct {
	cause     string
	transient bool
	// until is when a rate limit ends, as GitHub reported it. It is zero when
	// GitHub did not say.
	until time.Time
}

func (e *accountProbeError) Error() string { return e.cause }

var httpStatusError = regexp.MustCompile(`HTTP ([0-9]{3})`)

// probeRepository checks that a token can act on the room's repository. Errors
// never include gh output, since GitHub and CLI errors can contain credentials.
func probeRepository(ctx context.Context, cmd *exec.Cmd, token string, r repository, write bool) (bool, error) {
	body, denied, err := probeRequest(ctx, cmd, r.host, token, "repos/"+r.name)
	if err != nil || denied {
		return denied, err
	}
	if !write {
		return false, nil
	}
	var permissions struct {
		Permissions struct {
			Push *bool `json:"push"`
		} `json:"permissions"`
	}
	if json.Unmarshal(body, &permissions) != nil || permissions.Permissions.Push == nil {
		return false, &accountProbeError{cause: "GitHub API returned invalid repository permissions; retrying", transient: true}
	}
	return !*permissions.Permissions.Push, nil
}

// probeAccount reports the login a token belongs to. It reads no saved
// credential, so a managed shell can ask this with the token the turn gave it.
func probeAccount(ctx context.Context, cmd *exec.Cmd, host, token string) (string, bool, error) {
	body, denied, err := probeRequest(ctx, cmd, host, token, "user")
	if err != nil || denied {
		return "", denied, err
	}
	var account struct {
		Login string `json:"login"`
	}
	if json.Unmarshal(body, &account) != nil || account.Login == "" {
		return "", false, &accountProbeError{cause: "GitHub API returned no account login; retrying", transient: true}
	}
	return account.Login, false, nil
}

// probeRequest runs one token-authenticated API request and classifies the
// answer: denied when GitHub refuses this token, an accountProbeError when the
// answer is transient, and no error otherwise. The body has its headers removed.
func probeRequest(ctx context.Context, cmd *exec.Cmd, host, token, path string) ([]byte, bool, error) {
	raw, stderr, runErr := authResult(ctx, cmd, token, "api", "--hostname", host, path, "--include")
	// These checks are the requests a turn spends at startup, so the journal
	// counts them under their own caller.
	ghquota.RecordCaller("access-check", false)
	if ctx.Err() != nil {
		return nil, false, context.Cause(ctx)
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
		return body, false, nil
	}
	text := strings.ToLower(string(body) + string(stderr))
	limited := code == 429 || code == 403 && (headers.Get("Retry-After") != "" || headers.Get("X-RateLimit-Remaining") == "0" || strings.Contains(text, "rate limit") || strings.Contains(text, "abuse"))
	if limited {
		return nil, false, &accountProbeError{cause: fmt.Sprintf("GitHub API rate limited (HTTP %d); retrying", code), transient: true, until: rateLimitReset(headers, time.Now())}
	}
	if code == 401 || code == 403 || code == 404 {
		return nil, true, nil
	}
	if code == 0 {
		return nil, false, &accountProbeError{cause: "GitHub API probe failed (network or CLI error); retrying", transient: true}
	}
	return nil, false, &accountProbeError{cause: fmt.Sprintf("GitHub API probe failed (HTTP %d); retrying", code), transient: code >= 500}
}

// rateLimitReset reads when GitHub says the quota returns. A missing or
// unreadable header leaves the time unknown.
func rateLimitReset(headers http.Header, now time.Time) time.Time {
	if value := headers.Get("X-RateLimit-Reset"); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
			return time.Unix(seconds, 0)
		}
	}
	if value := headers.Get("Retry-After"); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
			return now.Add(time.Duration(seconds) * time.Second)
		}
	}
	return time.Time{}
}

func cacheAccount(key, login, config string) {
	accounts.Lock()
	accounts.choices[key] = choice{login: login, until: time.Now().Add(accountCacheTTL), config: config}
	accounts.Unlock()
}

// proveAccount records a choice a real access check confirmed. It lasts much
// longer than a cached one and survives the engine process, so the next turn,
// and the next engine start, make no GitHub API call.
func proveAccount(key, login, config string) {
	until := time.Now().Add(accountProvedTTL)
	accounts.Lock()
	accounts.choices[key] = choice{login: login, until: until, config: config}
	accounts.Unlock()
	rememberChoice(key, login, config, until)
}

// forgetAccount drops an account choice GitHub no longer accepts, and the rate
// limit window that belonged to it.
func forgetAccount(key string) {
	accounts.Lock()
	delete(accounts.choices, key)
	delete(accounts.limited, key)
	accounts.Unlock()
}

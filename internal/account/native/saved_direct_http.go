package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// UsageHTTPError reports only the HTTP status, never request or response data.
type UsageHTTPError struct{ Status int }

func (e *UsageHTTPError) Error() string {
	return fmt.Sprintf("Account usage could not be read (HTTP %d).", e.Status)
}

func directNow(now func() time.Time) time.Time {
	if now != nil {
		return now().UTC()
	}
	return time.Now().UTC()
}

func directStatusError(resp *http.Response, now time.Time) error {
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
		return nil
	}
	delay := time.Duration(0)
	value := resp.Header.Get("Retry-After")
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
		delay = time.Duration(seconds) * time.Second
	} else if until, err := http.ParseTime(value); err == nil && until.After(now) {
		delay = until.Sub(now)
	}
	return &QuotaError{Status: resp.StatusCode, RetryAfter: delay}
}

// directRequest never includes request data or provider response text in errors.
func directRequest(ctx context.Context, client claudeHTTPClient, method, endpoint, body string, headers map[string]string, now time.Time, authStatuses ...int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, errors.New("Account usage could not be requested.")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Account usage could not be reached.")
	}
	defer resp.Body.Close()
	if err := directStatusError(resp, now); err != nil {
		return nil, err
	}
	invalidAuth := resp.StatusCode == http.StatusUnauthorized
	for _, status := range authStatuses {
		invalidAuth = invalidAuth || resp.StatusCode == status
	}
	if invalidAuth {
		return nil, ErrInvalidGrant
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &UsageHTTPError{Status: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("Account usage response could not be read.")
	}
	return raw, nil
}

package cliproxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var ErrNotSignedIn = errors.New("not signed in")

type Result struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	Snippet  string `json:"snippet"`
	Provider string `json:"provider"`
}
type Query struct {
	Text  string
	Limit int
}

func capped(provider string, q Query, results []Result) []Result {
	if len(results) > q.Limit {
		results = results[:q.Limit]
	}
	for i := range results {
		results[i].Provider = provider
	}
	return results
}
func text(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 300 {
		return strings.TrimSpace(string(r[:300])) + "…"
	}
	return s
}

type searchRefusal struct {
	status  int
	message string
}

func (e searchRefusal) Error() string   { return e.message }
func (e searchRefusal) StatusCode() int { return e.status }

var searchSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{8,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9._\-]{8,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9._\-]{16,}`),
}

func searchRedact(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	for _, pattern := range searchSecretPatterns {
		s = pattern.ReplaceAllString(s, "[redacted]")
	}
	return s
}

func statusError(provider string, status int, body io.Reader, secrets ...string) error {
	// Read the whole bounded response before redacting and clipping. An oversized
	// response is discarded so a truncated secret cannot enter the error.
	raw, err := io.ReadAll(io.LimitReader(body, (64<<10)+1))
	reason := ""
	if err == nil && len(raw) <= 64<<10 {
		var refusal struct {
			Detail  string          `json:"detail"`
			Message string          `json:"message"`
			Error   json.RawMessage `json:"error"`
		}
		if json.Unmarshal(raw, &refusal) == nil {
			reason = strings.TrimSpace(refusal.Detail)
			if reason == "" {
				reason = strings.TrimSpace(refusal.Message)
			}
			if reason == "" {
				var message string
				if json.Unmarshal(refusal.Error, &message) == nil {
					reason = strings.TrimSpace(message)
				}
				if reason == "" {
					var nested struct {
						Message string `json:"message"`
					}
					if json.Unmarshal(refusal.Error, &nested) == nil {
						reason = strings.TrimSpace(nested.Message)
					}
				}
			}
		}
	}
	reason = text(searchRedact(reason, secrets...))
	message := fmt.Sprintf("%s answered HTTP %d", provider, status)
	if reason != "" {
		message += ": " + reason
	}
	return searchRefusal{status: status, message: message}
}

// DynaSearchAuth applies the same request access manager as the model routes.
func (s *Service) DynaSearchAuth(c *gin.Context) {
	if s == nil || s.accessManager == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	_, err := s.accessManager.Authenticate(c.Request.Context(), c.Request)
	if err != nil {
		c.AbortWithStatus(err.HTTPStatusCode())
		return
	}
	c.Next()
}

// DynaSearch selects only an active credential in the private request allowlist.
// Provider responses and errors are never allowed to expose that credential.
func (s *Service) DynaSearch(ctx context.Context, provider, query string, limit int, values []string) ([]Result, error) {
	allowed := make(map[string]bool)
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			decoded, err := base64.RawURLEncoding.DecodeString(part)
			if err == nil && len(decoded) > 0 {
				allowed[string(decoded)] = true
			}
		}
	}
	for _, auth := range s.coreManager.List() {
		expected := "kimi"
		if provider == "zai" {
			expected = "openai-compatible-zai"
		}
		if !allowed[auth.ID] || auth.Provider != expected || auth.Disabled || auth.Status != coreauth.StatusActive || auth.RequiresSignIn {
			continue
		}
		var secret, device string
		if provider == "kimi" {
			secret, _ = auth.Metadata["access_token"].(string)
			device, _ = auth.Metadata["device_id"].(string)
			if secret == "" {
				secret = auth.Attributes["access_token"]
			}
		} else {
			secret = auth.Attributes["api_key"]
		}
		if secret == "" {
			continue
		}
		q := Query{Text: query, Limit: limit}
		var results []Result
		var err error
		if provider == "kimi" {
			results, err = (Kimi{Token: func(context.Context) (string, error) { return secret, nil }, DeviceID: device}).Search(ctx, q)
		} else {
			results, err = (Zai{Key: func(context.Context) (string, error) { return secret, nil }}).Search(ctx, q)
		}
		if err != nil {
			if errors.Is(err, ErrNotSignedIn) {
				return nil, ErrNotSignedIn
			}
			var refusal searchRefusal
			if errors.As(err, &refusal) {
				return nil, searchRefusal{status: refusal.status, message: searchRedact(refusal.Error(), secret)}
			}
			return nil, errors.New("search provider unavailable")
		}
		for i := range results {
			results[i].Title = strings.ReplaceAll(results[i].Title, secret, "[redacted]")
			results[i].URL = strings.ReplaceAll(results[i].URL, secret, "[redacted]")
			results[i].Snippet = strings.ReplaceAll(results[i].Snippet, secret, "[redacted]")
		}
		return results, nil
	}
	return nil, ErrNotSignedIn
}

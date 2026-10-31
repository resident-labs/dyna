package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
)

var errSignInExpired = errors.New("sign-in expired")

type quotaEntry struct {
	at    time.Time
	usage contract.AccountUsage
}

type Quotas struct {
	client *http.Client
	key    string
	now    func() time.Time
	mu     sync.Mutex
	cache  map[string]quotaEntry
}

func (p *Proxy) Quotas(key string, now func() time.Time) *Quotas {
	return &Quotas{client: p.client, key: key, now: now, cache: make(map[string]quotaEntry)}
}

// request never returns management response headers or errors that could contain credentials.
func (q *Quotas) request(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://cliproxy/v0/management/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("management request failed")
	}
	req.Header.Set("Authorization", "Bearer "+q.key)
	req.Header.Set("Content-Type", "application/json")
	res, err := q.client.Do(req)
	if err != nil {
		return nil, errors.New("management request failed")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusBadRequest && bytes.Contains(data, []byte("auth token refresh failed")) {
		return nil, errSignInExpired
	}
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("management request failed")
	}
	if err != nil || !json.Valid(data) {
		return nil, errors.New("management response invalid")
	}
	return data, nil
}

func (q *Quotas) Read(ctx context.Context) (contract.AccountsQuotaResponse, error) {
	data, err := q.request(ctx, "GET", "auth-files", nil)
	if err != nil {
		return contract.AccountsQuotaResponse{}, err
	}
	var list struct {
		Files []struct {
			Index    string `json:"auth_index"`
			Provider string `json:"provider"`
			Label    string `json:"label"`
			Quota    struct {
				ObservedAt *time.Time        `json:"observed_at"`
				Signals    map[string]string `json:"signals"`
			} `json:"quota"`
		} `json:"files"`
	}
	if json.Unmarshal(data, &list) != nil {
		return contract.AccountsQuotaResponse{}, errors.New("management response invalid")
	}
	result := contract.AccountsQuotaResponse{Accounts: []contract.AccountUsage{}}
	for _, file := range list.Files {
		usage := contract.AccountUsage{Provider: file.Provider, Label: file.Label, Windows: []contract.UsageWindow{}}
		if file.Provider == "codex" && file.Quota.ObservedAt != nil && !file.Quota.ObservedAt.IsZero() {
			usage.ObservedAt = file.Quota.ObservedAt
			headers := http.Header{}
			for key, value := range file.Quota.Signals {
				headers.Set(key, value)
			}
			for _, name := range []string{"primary", "secondary"} {
				prefix := "X-Codex-" + name + "-"
				used, err := strconv.ParseFloat(headers.Get(prefix+"Used-Percent"), 64)
				if err != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 {
					continue
				}
				var seconds int64
				if minutes, err := strconv.ParseInt(headers.Get(prefix+"Window-Minutes"), 10, 32); err == nil && minutes > 0 {
					seconds = minutes * 60
				}
				var reset time.Time
				if n, err := strconv.ParseInt(headers.Get(prefix+"Reset-At"), 10, 64); err == nil && n > 0 {
					reset = time.Unix(n, 0).UTC()
				} else if n, err := strconv.ParseInt(headers.Get(prefix+"Reset-After-Seconds"), 10, 32); err == nil && n >= 0 {
					reset = file.Quota.ObservedAt.Add(time.Duration(n) * time.Second)
				}
				usage.Windows = append(usage.Windows, contract.UsageWindow{Name: name, UsedPercent: used, Seconds: seconds, ResetsAt: reset})
			}
		}
		if file.Provider != "claude" {
			result.Accounts = append(result.Accounts, usage)
			continue
		}
		q.mu.Lock()
		entry, ok := q.cache[file.Index]
		q.mu.Unlock()
		if ok && q.now().Sub(entry.at) < 5*time.Minute {
			usage = entry.usage
			usage.Label = file.Label
			result.Accounts = append(result.Accounts, usage)
			continue
		}
		payload, _ := json.Marshal(struct {
			Index  string            `json:"auth_index"`
			Method string            `json:"method"`
			URL    string            `json:"url"`
			Header map[string]string `json:"header"`
		}{file.Index, "GET", "https://api.anthropic.com/api/oauth/usage", map[string]string{
			"Authorization": "Bearer $TOKEN$", "anthropic-beta": "oauth-2025-04-20",
			"Accept": "application/json", "User-Agent": "dyna-account-manager/1.0",
		}})
		requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		data, err := q.request(requestCtx, "POST", "api-call", payload)
		cancel()
		var reply struct {
			Status int    `json:"status_code"`
			Body   string `json:"body"`
		}
		var buckets map[string]json.RawMessage
		switch {
		case errors.Is(err, errSignInExpired):
			usage.Cause = contract.UsageSignInExpired
		case err != nil || json.Unmarshal(data, &reply) != nil:
			usage.Cause = contract.UsageUnreachable
		case reply.Status == 401 || reply.Status == 403:
			usage.Cause = contract.UsageSignInExpired
		case reply.Status != 200 || json.Unmarshal([]byte(reply.Body), &buckets) != nil || buckets == nil:
			usage.Cause = contract.UsageUnreachable
		default:
			observedAt := q.now().UTC()
			usage.ObservedAt = &observedAt
			weekly := "seven_day_oauth_apps"
			if len(buckets[weekly]) == 0 || string(buckets[weekly]) == "null" {
				weekly = "seven_day"
			}
			for _, window := range []struct {
				key, name string
				seconds   int64
			}{{"five_hour", "five_hour", 18000}, {weekly, "seven_day", 604800}} {
				var b *struct {
					Utilization float64   `json:"utilization"`
					ResetsAt    time.Time `json:"resets_at"`
				}
				if raw := buckets[window.key]; len(raw) > 0 {
					if json.Unmarshal(raw, &b) != nil {
						usage.Cause = contract.UsageUnreachable
						usage.Windows = nil
						break
					}
					if b != nil {
						usage.Windows = append(usage.Windows, contract.UsageWindow{Name: window.name, UsedPercent: b.Utilization, Seconds: window.seconds, ResetsAt: b.ResetsAt})
					}
				}
			}
		}
		if usage.Cause != "" {
			usage.ObservedAt = nil
			// Log only JSON structure, never upstream values or response headers.
			var shape map[string]json.RawMessage
			_ = json.Unmarshal([]byte(reply.Body), &shape)
			kinds := make(map[string]string, len(shape))
			for key, raw := range shape {
				switch key {
				case "five_hour", "seven_day", "seven_day_oauth_apps", "limits", "error", "type", "message":
				default:
					continue
				}
				switch raw[0] {
				case '{':
					kinds[key] = "object"
				case '[':
					kinds[key] = "array"
				case '"':
					kinds[key] = "string"
				case 'n':
					kinds[key] = "null"
				case 't', 'f':
					kinds[key] = "boolean"
				default:
					kinds[key] = "number"
				}
			}
			log.Printf("model quota failed: upstream_status=%d keys_and_kinds=%q", reply.Status, kinds)
		}
		q.mu.Lock()
		q.cache[file.Index] = quotaEntry{at: q.now(), usage: usage}
		q.mu.Unlock()
		result.Accounts = append(result.Accounts, usage)
	}
	return result, nil
}

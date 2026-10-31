package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
)

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
func (q *Quotas) request(ctx context.Context, path string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://cliproxy/v0/management/"+path, nil)
	if err != nil {
		return nil, errors.New("management request failed")
	}
	req.Header.Set("Authorization", "Bearer "+q.key)
	res, err := q.client.Do(req)
	if err != nil {
		return nil, errors.New("management request failed")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("management request failed")
	}
	if err != nil || !json.Valid(data) {
		return nil, errors.New("management response invalid")
	}
	return data, nil
}

func (q *Quotas) Read(ctx context.Context) (contract.AccountsQuotaResponse, error) {
	data, err := q.request(ctx, "auth-files")
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
		requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		data, err := q.request(requestCtx, "auth-files/quota?auth_index="+url.QueryEscape(file.Index))
		cancel()
		var reply struct {
			Files []struct {
				Index      string     `json:"auth_index"`
				ObservedAt *time.Time `json:"observed_at"`
				Status     int        `json:"status_code"`
				Error      string     `json:"error"`
				Windows    []struct {
					Kind        string  `json:"kind"`
					Scope       string  `json:"scope"`
					Utilization float64 `json:"utilization"`
					ResetsAt    string  `json:"resets_at"`
				} `json:"windows"`
			} `json:"files"`
		}
		switch {
		case err != nil || json.Unmarshal(data, &reply) != nil || len(reply.Files) != 1 || reply.Files[0].Index != file.Index:
			usage.Cause = contract.UsageUnreachable
		case reply.Files[0].Status == http.StatusUnauthorized || reply.Files[0].Status == http.StatusForbidden:
			usage.Cause = contract.UsageSignInExpired
		case reply.Files[0].Status != http.StatusOK || reply.Files[0].Error != "":
			usage.Cause = contract.UsageUnreachable
		default:
			usage.ObservedAt = reply.Files[0].ObservedAt
			var weekly *contract.UsageWindow
			var oauthWeekly *contract.UsageWindow
			for _, w := range reply.Files[0].Windows {
				reset, _ := time.Parse(time.RFC3339Nano, w.ResetsAt)
				window := contract.UsageWindow{UsedPercent: w.Utilization, ResetsAt: reset}
				switch w.Kind {
				case "session":
					window.Name, window.Seconds = "five_hour", 18000
					usage.Windows = append(usage.Windows, window)
				case "weekly_all":
					window.Name, window.Seconds = "seven_day", 604800
					weekly = &window
				case "weekly_scoped":
					if strings.EqualFold(w.Scope, "Oauth Apps") {
						window.Name, window.Seconds = "seven_day", 604800
						oauthWeekly = &window
					} else if w.Scope != "" {
						window.Name, window.Seconds = w.Scope+" Weekly", 604800
						usage.Windows = append(usage.Windows, window)
					}
				}
			}
			if oauthWeekly != nil {
				weekly = oauthWeekly
			}
			if weekly != nil {
				usage.Windows = append([]contract.UsageWindow{*weekly}, usage.Windows...)
			}
		}
		q.mu.Lock()
		q.cache[file.Index] = quotaEntry{at: q.now(), usage: usage}
		q.mu.Unlock()
		result.Accounts = append(result.Accounts, usage)
	}
	return result, nil
}

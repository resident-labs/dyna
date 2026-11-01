package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
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
	management managementClient
	store      AccountStore
	now        func() time.Time
	mu         sync.Mutex
	cache      map[string]quotaEntry
	generation uint64
}

func (p *Proxy) Quotas(key string, st AccountStore, now func() time.Time) *Quotas {
	return &Quotas{management: managementClient{client: p.client, key: key}, store: st, now: now, cache: make(map[string]quotaEntry)}
}

// Clear drops observations after an account is removed.
func (q *Quotas) Clear() {
	q.mu.Lock()
	clear(q.cache)
	q.generation++
	q.mu.Unlock()
}

// ObservedState uses the existing bounded usage cache; it never starts a usage read.
func (q *Quotas) ObservedState(index string) CredentialState {
	q.mu.Lock()
	entry, ok := q.cache[index]
	q.mu.Unlock()
	if !ok || q.now().Sub(entry.at) >= 5*time.Minute {
		return CredentialUnknown
	}
	if entry.usage.Cause == contract.UsageSignInExpired {
		return CredentialExpired
	}
	if entry.usage.Cause == "" {
		return CredentialConnected
	}
	return CredentialUnknown
}

// CachedUsage reads an existing observation only. It never calls the proxy.
func (q *Quotas) CachedUsage(id contract.AccountID) (contract.AccountUsage, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, entry := range q.cache {
		if entry.usage.AccountID == id && q.now().Sub(entry.at) < 5*time.Minute {
			return entry.usage, true
		}
	}
	return contract.AccountUsage{}, false
}

func (q *Quotas) request(ctx context.Context, path string) (json.RawMessage, error) {
	return q.management.request(ctx, http.MethodGet, "/v0/management/"+path, nil)
}

func (q *Quotas) Read(ctx context.Context) (contract.AccountsQuotaResponse, error) {
	q.mu.Lock()
	initialGeneration := q.generation
	q.mu.Unlock()
	list, err := q.management.credentials(ctx)
	if err != nil {
		return contract.AccountsQuotaResponse{}, err
	}
	bindings, err := q.store.Accounts(ctx)
	if err != nil {
		return contract.AccountsQuotaResponse{}, errors.New("accounts unavailable")
	}
	byID := make(map[string]contract.Account, len(bindings))
	for _, binding := range bindings {
		byID[binding.ProxyID] = binding.Account
	}
	result := contract.AccountsQuotaResponse{Accounts: []contract.AccountUsage{}}
	for _, file := range list {
		account, ok := byID[file.ID]
		if !ok {
			continue
		}
		usage := contract.AccountUsage{AccountID: account.ID, Provider: file.Provider, Label: account.Label, Windows: []contract.UsageWindow{}}
		if file.Provider == "codex" && file.Quota.ObservedAt != nil && !file.Quota.ObservedAt.IsZero() {
			usage.ObservedAt = file.Quota.ObservedAt
			for _, item := range []struct {
				name string
				data CredentialQuotaWindow
			}{{"primary", file.Quota.Primary}, {"secondary", file.Quota.Secondary}} {
				if item.data.UsedPercent == nil {
					continue
				}
				window := contract.UsageWindow{Name: item.name, Kind: contract.UsageWindowOther, UsedPercent: *item.data.UsedPercent}
				if item.data.WindowMinutes != nil {
					window.Seconds = int64(*item.data.WindowMinutes) * 60
					switch window.Seconds {
					case 18000:
						window.Kind = contract.UsageWindowFiveHour
					case 604800:
						window.Kind = contract.UsageWindowWeekly
					}
				}
				if item.data.ResetAt != nil {
					window.ResetsAt = time.Unix(*item.data.ResetAt, 0).UTC()
				} else if item.data.ResetAfterSeconds != nil {
					window.ResetsAt = file.Quota.ObservedAt.Add(time.Duration(*item.data.ResetAfterSeconds) * time.Second)
				}
				usage.Windows = append(usage.Windows, window)
			}
		}
		if file.Provider != "claude" {
			slices.SortStableFunc(usage.Windows, func(a, b contract.UsageWindow) int {
				if a.Kind == contract.UsageWindowFiveHour && b.Kind != contract.UsageWindowFiveHour {
					return -1
				}
				if b.Kind == contract.UsageWindowFiveHour && a.Kind != contract.UsageWindowFiveHour {
					return 1
				}
				return 0
			})
			q.mu.Lock()
			if q.generation == initialGeneration {
				q.cache[file.Index] = quotaEntry{at: q.now(), usage: usage}
			}
			q.mu.Unlock()
			result.Accounts = append(result.Accounts, usage)
			continue
		}
		q.mu.Lock()
		entry, ok := q.cache[file.Index]
		generation := q.generation
		q.mu.Unlock()
		if ok && q.now().Sub(entry.at) < 5*time.Minute {
			usage = entry.usage
			usage.Label = account.Label
			usage.AccountID = account.ID
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
			var session *contract.UsageWindow
			var weekly *contract.UsageWindow
			var oauthWeekly *contract.UsageWindow
			for _, w := range reply.Files[0].Windows {
				reset, _ := time.Parse(time.RFC3339Nano, w.ResetsAt)
				window := contract.UsageWindow{UsedPercent: w.Utilization, ResetsAt: reset}
				switch w.Kind {
				case "session":
					window.Name, window.Kind, window.Seconds = "five_hour", contract.UsageWindowFiveHour, 18000
					session = &window
				case "weekly_all":
					window.Name, window.Kind, window.Seconds = "seven_day", contract.UsageWindowWeekly, 604800
					weekly = &window
				case "weekly_scoped":
					if strings.EqualFold(w.Scope, "Oauth Apps") {
						window.Name, window.Kind, window.Seconds = "seven_day", contract.UsageWindowWeekly, 604800
						oauthWeekly = &window
					} else if w.Scope != "" {
						window.Name, window.Kind, window.Model, window.Seconds = w.Scope+" Weekly", contract.UsageWindowWeekly, w.Scope, 604800
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
			if session != nil {
				usage.Windows = append([]contract.UsageWindow{*session}, usage.Windows...)
			}
		}
		q.mu.Lock()
		if q.generation == generation {
			q.cache[file.Index] = quotaEntry{at: q.now(), usage: usage}
		}
		q.mu.Unlock()
		result.Accounts = append(result.Accounts, usage)
	}
	return result, nil
}

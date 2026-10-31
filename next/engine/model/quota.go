package model

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

var errSignInExpired = errors.New("sign-in expired")

type quotaEntry struct {
	at    time.Time
	usage contract.AccountUsage
}

type Quotas struct {
	management managementClient
	store      *store.Store
	now        func() time.Time
	mu         sync.Mutex
	cache      map[string]quotaEntry
	generation uint64
}

func (p *Proxy) Quotas(key string, st *store.Store, now func() time.Time) *Quotas {
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

func (q *Quotas) request(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	return q.management.request(ctx, method, "/v0/management/"+path, body)
}

func (q *Quotas) Read(ctx context.Context) (contract.AccountsQuotaResponse, error) {
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
		if file.Provider != "claude" {
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
		payload, _ := json.Marshal(struct {
			Index  string            `json:"auth_index"`
			Method string            `json:"method"`
			URL    string            `json:"url"`
			Header map[string]string `json:"header"`
		}{file.Index, "GET", "https://api.anthropic.com/api/oauth/usage", map[string]string{
			"Authorization": "Bearer $TOKEN$", "anthropic-beta": "oauth-2025-04-20",
			"Accept": "application/json", "User-Agent": "dyna-account-manager/1.0",
		}})
		data, err := q.request(ctx, "POST", "api-call", payload)
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
		if q.generation == generation {
			q.cache[file.Index] = quotaEntry{at: q.now(), usage: usage}
		}
		q.mu.Unlock()
		result.Accounts = append(result.Accounts, usage)
	}
	return result, nil
}

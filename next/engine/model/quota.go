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
	"golang.org/x/sync/singleflight"
)

// quotaReads bounds concurrent usage reads across all callers.
const quotaReads = 4

type quotaEntry struct {
	at    time.Time
	usage contract.AccountUsage
	// passive marks a reading that proves nothing about the sign-in, so ObservedState
	// ignores it: the provider reported nothing the engine could read.
	passive bool
}

type Quotas struct {
	management managementClient
	store      AccountStore
	now        func() time.Time
	slots      chan struct{}
	reads      singleflight.Group
	mu         sync.Mutex
	cache      map[string]quotaEntry
	generation uint64
}

func (p *Proxy) Quotas(key string, st AccountStore, now func() time.Time) *Quotas {
	return &Quotas{management: managementClient{client: p.client, key: key}, store: st, now: now, slots: make(chan struct{}, quotaReads), cache: make(map[string]quotaEntry)}
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
	if !ok || entry.passive || q.now().Sub(entry.at) >= 5*time.Minute {
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
	bound := slices.DeleteFunc(list, func(file Credential) bool {
		_, ok := byID[file.ID]
		return !ok
	})
	result := contract.AccountsQuotaResponse{Accounts: make([]contract.AccountUsage, len(bound))}
	var reads sync.WaitGroup
	for i, file := range bound {
		reads.Go(func() {
			account := byID[file.ID]
			usage := q.usage(ctx, file, account)
			usage.Label = account.Label
			result.Accounts[i] = usage
		})
	}
	reads.Wait()
	q.markActive(ctx, bound, result.Accounts)
	return result, nil
}

// RoutingPlace is one credential in the proxy's routing order. Rank 1 takes new work,
// and 0 means routing cannot use the credential now.
type RoutingPlace struct {
	Index string `json:"auth_index"`
	Rank  int    `json:"rank"`
	Spent bool   `json:"spent"`
}

// Routing reads the order the proxy's selector would use, by credential index. It costs
// no provider request. ok is false when the proxy reports no order.
func (q *Quotas) Routing(ctx context.Context) (map[string]RoutingPlace, bool) {
	data, err := q.management.request(ctx, http.MethodGet, "/v8/management/routing/status", nil)
	if err != nil {
		return nil, false
	}
	var status struct {
		Providers map[string][]RoutingPlace `json:"providers"`
	}
	if json.Unmarshal(data, &status) != nil || len(status.Providers) == 0 {
		return nil, false
	}
	places := make(map[string]RoutingPlace)
	for _, provider := range status.Providers {
		for _, place := range provider {
			places[place.Index] = place
		}
	}
	return places, true
}

// markActive reads after the usage reads, which publish to routing. Without an order
// nothing is hidden on a guess, so every account counts as active.
func (q *Quotas) markActive(ctx context.Context, files []Credential, usage []contract.AccountUsage) {
	places, ok := q.Routing(ctx)
	if !ok {
		for i := range usage {
			usage[i].Active = true
		}
		return
	}
	pins, err := q.store.PinnedAccounts(ctx)
	if err != nil {
		pins = nil
	}
	for i, file := range files {
		place := places[file.Index]
		usage[i].Active = !place.Spent && (place.Rank == 1 || (place.Rank > 0 && slices.Contains(pins, usage[i].AccountID)))
	}
}

// usage returns the cached reading or reads the proxy, one read per credential at a time.
func (q *Quotas) usage(ctx context.Context, file Credential, account contract.Account) contract.AccountUsage {
	if entry, ok := q.cached(file.Index); ok {
		return entry.usage
	}
	value, _, _ := q.reads.Do(file.Index, func() (any, error) {
		select {
		case q.slots <- struct{}{}:
			defer func() { <-q.slots }()
		case <-ctx.Done():
			return contract.AccountUsage{AccountID: account.ID, Provider: file.Provider, Windows: []contract.UsageWindow{}, Cause: contract.UsageUnreachable}, nil
		}
		if entry, ok := q.cached(file.Index); ok {
			return entry.usage, nil
		}
		q.mu.Lock()
		generation := q.generation
		q.mu.Unlock()
		// A shared read outlives the caller that started it, and a canceled read must not stay cached.
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		data, err := q.request(readCtx, "auth-files/quota?auth_index="+url.QueryEscape(file.Index))
		usage, passive := quotaUsage(file, account.ID, data, err)
		q.mu.Lock()
		if q.generation == generation {
			q.cache[file.Index] = quotaEntry{at: q.now(), usage: usage, passive: passive}
		}
		q.mu.Unlock()
		return usage, nil
	})
	return value.(contract.AccountUsage)
}

func (q *Quotas) cached(index string) (quotaEntry, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry, ok := q.cache[index]
	return entry, ok && q.now().Sub(entry.at) < 5*time.Minute
}

type quotaWindow struct {
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Scope       string  `json:"scope"`
	Seconds     int64   `json:"seconds"`
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

type quotaFile struct {
	Index      string                  `json:"auth_index"`
	ObservedAt *time.Time              `json:"observed_at"`
	Status     int                     `json:"status_code"`
	Error      string                  `json:"error"`
	State      string                  `json:"state"`
	Windows    []quotaWindow           `json:"windows"`
	Balances   []contract.UsageBalance `json:"balances"`
}

// quotaUsage normalizes one proxy reply. Only Claude leaves state empty; its status
// codes still decide the sign-in. A provider's reader never reports an expired sign-in.
func quotaUsage(file Credential, account contract.AccountID, data json.RawMessage, err error) (contract.AccountUsage, bool) {
	usage := contract.AccountUsage{AccountID: account, Provider: file.Provider, Windows: []contract.UsageWindow{}}
	var reply struct {
		Files []quotaFile `json:"files"`
	}
	if err != nil || json.Unmarshal(data, &reply) != nil || len(reply.Files) != 1 || reply.Files[0].Index != file.Index {
		usage.Cause = contract.UsageUnreachable
		return usage, false
	}
	reading := reply.Files[0]
	switch reading.State {
	case "":
		claudeUsage(reading, &usage)
		return usage, false
	case "ok":
		usage.ObservedAt = reading.ObservedAt
		usage.Balances = reading.Balances
		for _, w := range reading.Windows {
			reset, _ := time.Parse(time.RFC3339Nano, w.ResetsAt)
			usage.Windows = append(usage.Windows, contract.UsageWindow{Name: w.Name, Kind: contract.UsageWindowKind(w.Kind), Model: w.Scope, UsedPercent: w.Utilization, Seconds: w.Seconds, ResetsAt: reset})
		}
		slices.SortStableFunc(usage.Windows, func(a, b contract.UsageWindow) int {
			if a.Kind == contract.UsageWindowFiveHour && b.Kind != contract.UsageWindowFiveHour {
				return -1
			}
			if b.Kind == contract.UsageWindowFiveHour && a.Kind != contract.UsageWindowFiveHour {
				return 1
			}
			return 0
		})
		return usage, false
	case "unknown":
		return usage, true
	case "rate_limited":
		usage.Cause = contract.UsageRateLimited
		return usage, false
	default:
		usage.Cause = contract.UsageUnreachable
		return usage, false
	}
}

func claudeUsage(reading quotaFile, usage *contract.AccountUsage) {
	switch {
	case reading.Status == http.StatusUnauthorized || reading.Status == http.StatusForbidden:
		usage.Cause = contract.UsageSignInExpired
		return
	case reading.Status != http.StatusOK || reading.Error != "":
		usage.Cause = contract.UsageUnreachable
		return
	}
	usage.ObservedAt = reading.ObservedAt
	var session *contract.UsageWindow
	var weekly *contract.UsageWindow
	var oauthWeekly *contract.UsageWindow
	for _, w := range reading.Windows {
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

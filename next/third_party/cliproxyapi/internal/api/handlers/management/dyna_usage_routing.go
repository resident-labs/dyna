package management

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"golang.org/x/sync/singleflight"
)

// Every usage read goes through one path, so Home's reads and routing's refreshes
// share their answers and a credential is never read twice at once. The selector
// sees only the readings published to the auth manager: it cannot import the
// readers, and it must never wait for one.
const (
	// dynaUsageShareAge matches the engine's usage cache, so Home sees no older
	// figures than before, and a refresh just before a Home load costs one read.
	dynaUsageShareAge = 5 * time.Minute
	// dynaUsageReadLimit bounds a read nobody waits on. Without it a stalled read
	// would hold its provider's refresh open for good.
	dynaUsageReadLimit = 30 * time.Second
)

type dynaUsageShare struct {
	mu      sync.Mutex
	entries map[string]dynaSharedUsage
	reads   singleflight.Group
}

type dynaSharedUsage struct {
	at    time.Time
	entry authFileQuotaEntry
}

func (s *dynaUsageShare) recent(key string, now time.Time) (authFileQuotaEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	shared, ok := s.entries[key]
	return shared.entry, ok && now.Sub(shared.at) < dynaUsageShareAge
}

func (s *dynaUsageShare) keep(key string, entry authFileQuotaEntry, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for old, shared := range s.entries {
		if now.Sub(shared.at) >= dynaUsageShareAge {
			delete(s.entries, old)
		}
	}
	if s.entries == nil {
		s.entries = make(map[string]dynaSharedUsage)
	}
	s.entries[key] = dynaSharedUsage{at: now, entry: entry}
}

// sharedQuota answers from a recent read or joins one in flight. A failed read is
// shared too, so a provider that just refused is not asked again at once. The read
// outlives the caller that started it, and every read publishes to routing.
func (h *Handler) sharedQuota(ctx context.Context, cfg *config.Config, manager *coreauth.Manager, auth *coreauth.Auth) authFileQuotaEntry {
	key := auth.ID + "\x00" + strconv.FormatUint(auth.RegistrationEpoch, 10)
	if entry, ok := h.dynaUsage.recent(key, time.Now()); ok {
		return entry
	}
	value, _, _ := h.dynaUsage.reads.Do(key, func() (any, error) {
		if entry, ok := h.dynaUsage.recent(key, time.Now()); ok {
			return entry, nil
		}
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dynaUsageReadLimit)
		defer cancel()
		var entry authFileQuotaEntry
		if source, ok := dynaUsageSourceFor(auth); ok {
			entry = h.readDynaUsage(readCtx, source, auth)
		} else {
			entry = fetchClaudeOAuthQuota(readCtx, cfg, auth)
		}
		h.dynaUsage.keep(key, entry, time.Now())
		if hint, ok := dynaQuotaHint(entry); ok {
			manager.PublishQuotaReading(auth.ID, hint)
		}
		return entry, nil
	})
	return value.(authFileQuotaEntry)
}

// bindDynaRefresh gives cold picks a way to ask for readings without a request waiting.
func (h *Handler) bindDynaRefresh(manager *coreauth.Manager) {
	manager.SetQuotaRefresher(h.refreshDynaUsage)
}

// refreshDynaUsage reads one provider's credentials and reports a 429 from any of them.
func (h *Handler) refreshDynaUsage(provider string) bool {
	h.mu.Lock()
	manager, cfg := h.authManager, h.cfg
	h.mu.Unlock()
	if manager == nil {
		return false
	}
	var limited atomic.Bool
	var reads sync.WaitGroup
	slots := make(chan struct{}, authFileQuotaConcurrency)
	for _, auth := range manager.List() {
		if auth == nil || auth.Disabled || !strings.EqualFold(strings.TrimSpace(auth.Provider), provider) || !dynaQuotaReadable(auth) {
			continue
		}
		reads.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			entry := h.sharedQuota(context.Background(), cfg, manager, auth)
			if entry.State == dynaUsageRateLimited || entry.StatusCode == http.StatusTooManyRequests {
				limited.Store(true)
			}
		})
	}
	reads.Wait()
	return limited.Load()
}

// dynaQuotaReadable matches the credentials GetAuthFileQuota reads.
func dynaQuotaReadable(auth *coreauth.Auth) bool {
	if _, ok := dynaUsageSourceFor(auth); ok {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") &&
		auth.AuthKind() == coreauth.AuthKindOAuth && tokenValueFromMetadata(auth.Metadata) != ""
}

// dynaQuotaHint turns a reader's answer into a reading. A failed or empty answer gives
// none, so routing keeps the last reading until its reset passes.
func dynaQuotaHint(entry authFileQuotaEntry) (coreauth.QuotaResetHint, bool) {
	observed, errParse := time.Parse(time.RFC3339, entry.ObservedAt)
	if errParse != nil || entry.Error != "" {
		return coreauth.QuotaResetHint{}, false
	}
	hint := coreauth.QuotaResetHint{ObservedAt: observed}
	switch entry.State {
	case "":
		if entry.StatusCode != http.StatusOK {
			return coreauth.QuotaResetHint{}, false
		}
		hint.Windows = dynaClaudeResetWindows(entry.Windows)
	case dynaUsageOK:
		for _, window := range entry.Windows {
			hint.Windows = append(hint.Windows, coreauth.QuotaResetWindow{Model: window.Scope, Seconds: window.Seconds, UsedPercent: window.Utilization, ResetAt: dynaResetTime(window.ResetsAt)})
		}
		hint.Prepaid = len(entry.Windows) == 0 && len(entry.Balances) != 0
		hint.Empty = len(entry.Balances) != 0
		for _, balance := range entry.Balances {
			if amount, errAmount := strconv.ParseFloat(balance.Amount, 64); errAmount != nil || amount > 0 {
				hint.Empty = false
			}
		}
	default:
		return coreauth.QuotaResetHint{}, false
	}
	return hint, true
}

// dynaClaudeResetWindows reads Claude's vocabulary as the engine does for Home, so
// routing and the usage cards agree on which weekly window counts.
func dynaClaudeResetWindows(windows []authFileQuotaWindow) []coreauth.QuotaResetWindow {
	var out []coreauth.QuotaResetWindow
	var weekly, oauthApps *coreauth.QuotaResetWindow
	for _, window := range windows {
		read := coreauth.QuotaResetWindow{Seconds: dynaWeekSeconds, UsedPercent: window.Utilization, ResetAt: dynaResetTime(window.ResetsAt)}
		switch window.Kind {
		case "session":
			read.Seconds = dynaFiveHourSeconds
			out = append(out, read)
		case "weekly_all":
			weekly = &read
		case "weekly_scoped":
			if strings.EqualFold(window.Scope, "Oauth Apps") {
				oauthApps = &read
			} else if window.Scope != "" {
				read.Model = window.Scope
				out = append(out, read)
			}
		}
	}
	if oauthApps != nil {
		weekly = oauthApps
	}
	if weekly != nil {
		out = append(out, *weekly)
	}
	return out
}

// dynaResetTime reads a reset, leaving Go's zero time for an unused window as zero.
func dynaResetTime(value string) time.Time {
	reset, errParse := time.Parse(time.RFC3339Nano, value)
	if errParse != nil || reset.Unix() <= 0 {
		return time.Time{}
	}
	return reset
}

// GetRoutingStatus reports the routing order, computed by the selector's own ranking,
// so the engine shows the account routing would pick instead of working it out again.
func (h *Handler) GetRoutingStatus(c *gin.Context) {
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	c.JSON(http.StatusOK, manager.QuotaRoutingSnapshot())
}

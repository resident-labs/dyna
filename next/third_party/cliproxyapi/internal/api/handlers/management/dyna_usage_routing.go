package management

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
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
	entries map[string]*dynaSharedUsage
	reads   singleflight.Group
}

type dynaSharedUsage struct {
	at          time.Time // zero while the request is in flight
	entry       authFileQuotaEntry
	invalidated atomic.Bool
}

func (s *dynaUsageShare) recent(key string, now time.Time) *dynaSharedUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if read := s.entries[key]; read != nil && !read.at.IsZero() && now.Sub(read.at) < dynaUsageShareAge {
		return read
	}
	return nil
}

func dynaUsageKey(authID string, registration uint64) string {
	return authID + "\x00" + strconv.FormatUint(registration, 10)
}

func (s *dynaUsageShare) invalidate(authID string, registration uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := dynaUsageKey(authID, registration)
	if read := s.entries[key]; read != nil {
		read.invalidated.Store(true)
		delete(s.entries, key)
	}
}

// sharedQuota answers from a recent read or joins one in flight. A failed read is
// shared too, so a provider that just refused is not asked again at once. The read
// outlives the caller that started it. Invalidated reads never publish to routing.
func (h *Handler) sharedQuota(ctx context.Context, cfg *config.Config, manager *coreauth.Manager, auth *coreauth.Auth) authFileQuotaEntry {
	return h.readSharedQuota(ctx, cfg, manager, auth, false)
}

func (h *Handler) readSharedQuota(ctx context.Context, cfg *config.Config, manager *coreauth.Manager, auth *coreauth.Auth, afterRefusal bool) authFileQuotaEntry {
	s := &h.dynaUsage
	key := dynaUsageKey(auth.ID, auth.RegistrationEpoch)
	if read := s.recent(key, time.Now()); read != nil {
		return read.entry
	}
	for {
		started := false
		value, _, _ := s.reads.Do(key, func() (any, error) {
			if read := s.recent(key, time.Now()); read != nil {
				return read, nil
			}
			started = true
			read := new(dynaSharedUsage)
			s.mu.Lock()
			for old, shared := range s.entries {
				if !shared.at.IsZero() && time.Since(shared.at) >= dynaUsageShareAge {
					delete(s.entries, old)
				}
			}
			if s.entries == nil {
				s.entries = make(map[string]*dynaSharedUsage)
			}
			s.entries[key] = read
			s.mu.Unlock()
			readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dynaUsageReadLimit)
			defer cancel()
			if source, ok := dynaUsageSourceFor(auth); ok {
				read.entry = h.readDynaUsage(readCtx, source, auth)
			} else {
				read.entry = fetchClaudeOAuthQuota(readCtx, cfg, auth)
			}
			if hint, ok := dynaQuotaHint(read.entry); ok {
				manager.PublishQuotaReadingForAuth(auth, hint, func() bool { return !read.invalidated.Load() })
			}
			s.mu.Lock()
			read.at = time.Now()
			s.mu.Unlock()
			return read, nil
		})
		read := value.(*dynaSharedUsage)
		if !afterRefusal || started || !read.invalidated.Load() {
			return read.entry
		}
		// A targeted refresh joined an older read invalidated by the refusal.
		// Wait for that flight to finish before starting its replacement. If
		// our own read was invalidated, the newer refusal queues the next read.
		afterRefusal = false
	}
}

// bindDynaRefresh gives cold picks a way to ask for readings without a request waiting.
func (h *Handler) bindDynaRefresh(manager *coreauth.Manager) {
	manager.SetQuotaRefresher(h.refreshDynaUsage, h.dynaUsage.invalidate)
}

// refreshDynaUsage reads the requested credentials, or the provider pool if
// authIDs is empty, and reports a 429 from any of them.
func (h *Handler) refreshDynaUsage(provider string, authIDs []string) bool {
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
		if auth == nil || auth.Disabled || (len(authIDs) != 0 && !slices.Contains(authIDs, auth.ID)) || !strings.EqualFold(strings.TrimSpace(auth.Provider), provider) || !dynaQuotaReadable(auth) {
			continue
		}
		reads.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			entry := h.readSharedQuota(context.Background(), cfg, manager, auth, len(authIDs) != 0)
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
			read := coreauth.QuotaResetWindow{Model: window.Scope, Seconds: window.Seconds, UsedPercent: window.Utilization, ResetAt: dynaResetTime(window.ResetsAt)}
			if len(window.Models) != 0 && strings.EqualFold(entry.Provider, "antigravity") {
				// The display label is not a model ID. Publish the same limit for
				// each pool member so routing still matches its qualified ID.
				for _, model := range window.Models {
					read.Model = antigravity.DynaModelPrefix + "/" + model
					hint.Windows = append(hint.Windows, read)
				}
			} else {
				hint.Windows = append(hint.Windows, read)
			}
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

package management

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Dyna extends auth-files/quota with one reader per provider. Live shapes are
// unconfirmed, so a parser that cannot read a value leaves it out: the reading
// is then unknown, never zero. Readers use the stored credential and never
// refresh it; the auth manager owns renewal.
const (
	dynaUsageOK          = "ok"
	dynaUsageUnknown     = "unknown"
	dynaUsageUnreachable = "unreachable"
	dynaUsageRateLimited = "rate_limited"

	dynaUsageBodyLimit = 1 << 20

	dynaFiveHourSeconds = 5 * 3600
	dynaWeekSeconds     = 7 * 86400
	dynaMonthSeconds    = 30 * 86400
)

// dynaQuotaEntryFields extends authFileQuotaEntry. State is empty only for
// Claude, whose status code keeps its meaning for the engine.
type dynaQuotaEntryFields struct {
	State    string             `json:"state,omitempty"`
	Balances []dynaQuotaBalance `json:"balances,omitempty"`
}

// dynaQuotaWindowFields extends authFileQuotaWindow. Non-Claude windows use the
// kinds five_hour, weekly, monthly and other.
type dynaQuotaWindowFields struct {
	Name    string `json:"name,omitempty"`
	Seconds int64  `json:"seconds,omitempty"`
}

// dynaQuotaBalance is a prepaid balance. It is for display, not a limit.
type dynaQuotaBalance struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type dynaUsage struct {
	windows  []authFileQuotaWindow
	balances []dynaQuotaBalance
}

// dynaUsageSource reads one provider. request returns no URL when the credential
// has nothing to send. absent is a status meaning the account has no such plan.
// observed replaces the request for a provider that reports usage only in
// responses to model traffic.
type dynaUsageSource struct {
	request  func(*coreauth.Auth) (string, http.Header)
	parse    func(gjson.Result, time.Time) dynaUsage
	absent   int
	observed func(*coreauth.Auth, time.Time) dynaUsage
}

var dynaUsageSources = map[string]dynaUsageSource{
	"codex": {request: requestCodexUsage, parse: parseCodexUsage},
	"kimi":  {request: requestKimiUsage, parse: parseKimiUsage},
}

// dynaUsageTransport is a variable so tests never reach a provider.
var dynaUsageTransport = func(h *Handler, auth *coreauth.Auth) http.RoundTripper {
	return h.apiCallTransport(auth, "")
}

func dynaUsageSourceFor(auth *coreauth.Auth) (dynaUsageSource, bool) {
	source, ok := dynaUsageSources[strings.ToLower(strings.TrimSpace(auth.Provider))]
	return source, ok
}

func (h *Handler) readDynaUsage(ctx context.Context, source dynaUsageSource, auth *coreauth.Auth) authFileQuotaEntry {
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	now := time.Now()
	entry := authFileQuotaEntry{
		Name:       name,
		AuthIndex:  lockedAuthIndex(auth),
		Provider:   strings.TrimSpace(auth.Provider),
		Email:      authEmail(auth),
		ObservedAt: now.UTC().Format(time.RFC3339),
	}
	entry.State = dynaUsageUnknown
	if auth.Disabled {
		return entry
	}
	if source.observed != nil {
		entry.setUsage(source.observed(auth, now))
		if !auth.Quota.ObservedAt.IsZero() {
			entry.ObservedAt = auth.Quota.ObservedAt.UTC().Format(time.RFC3339)
		}
		return entry
	}
	url, header := source.request(auth)
	if url == "" {
		return entry
	}
	status, body, errFetch := h.fetchDynaUsage(ctx, auth, url, header)
	if errFetch != nil {
		log.WithError(errFetch).Debug("management usage request failed")
		entry.State = dynaUsageUnreachable
		entry.Error = "request failed"
		return entry
	}
	entry.StatusCode = status
	switch {
	case status == http.StatusOK:
		if gjson.ValidBytes(body) {
			entry.setUsage(source.parse(gjson.ParseBytes(body), now))
		}
	case source.absent != 0 && status == source.absent:
	case status == http.StatusTooManyRequests:
		entry.State = dynaUsageRateLimited
		entry.Error = fmt.Sprintf("upstream returned status %d", status)
	default:
		entry.State = dynaUsageUnreachable
		entry.Error = fmt.Sprintf("upstream returned status %d", status)
	}
	return entry
}

func (h *Handler) fetchDynaUsage(ctx context.Context, auth *coreauth.Auth, url string, header http.Header) (int, []byte, error) {
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if errRequest != nil {
		return 0, nil, errRequest
	}
	request.Header = header
	client := &http.Client{
		Transport: dynaUsageTransport(h, auth),
		// A redirect could carry the credential to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, errDo := client.Do(request)
	if errDo != nil {
		return 0, nil, errDo
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("management usage response close failed")
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(response.Body, dynaUsageBodyLimit))
	if errRead != nil {
		return 0, nil, errRead
	}
	return response.StatusCode, body, nil
}

func (e *authFileQuotaEntry) setUsage(usage dynaUsage) {
	seen := make(map[string]struct{}, len(usage.windows))
	for _, window := range usage.windows {
		if _, repeated := seen[window.Name]; repeated {
			continue
		}
		seen[window.Name] = struct{}{}
		e.Windows = append(e.Windows, window)
	}
	e.Balances = usage.balances
	if len(e.Windows) != 0 || len(e.Balances) != 0 {
		e.State = dynaUsageOK
	}
}

func (u *dynaUsage) addWindow(seconds int64, scope string, used float64, reset, now time.Time) {
	kind, name := dynaWindowKind(seconds)
	if scope != "" {
		name = scope + " " + name
	}
	window := authFileQuotaWindow{Kind: kind, Utilization: used, Scope: scope}
	window.Name, window.Seconds = name, seconds
	if dynaResetPossible(reset, seconds, now) {
		window.ResetsAt = reset.UTC().Format(time.RFC3339)
	}
	u.windows = append(u.windows, window)
}

func dynaWindowKind(seconds int64) (kind, name string) {
	switch {
	case seconds == dynaFiveHourSeconds:
		return "five_hour", "five_hour"
	case seconds == dynaWeekSeconds:
		return "weekly", "seven_day"
	case seconds >= 28*86400 && seconds <= 31*86400:
		return "monthly", "monthly"
	default:
		return "other", "window_" + strconv.FormatInt(seconds, 10)
	}
}

// dynaResetPossible drops a reset that has passed or lies beyond the window's
// own length, with a tenth to spare for calendar months.
func dynaResetPossible(reset time.Time, seconds int64, now time.Time) bool {
	if !reset.After(now) {
		return false
	}
	longest := 366 * 24 * time.Hour
	if seconds > 0 {
		longest = time.Duration(seconds+seconds/10+60) * time.Second
	}
	return reset.Sub(now) <= longest
}

// dynaPercent reads a number that a provider may send as a string. A missing or
// negative value is not a reading.
func dynaPercent(value gjson.Result) (float64, bool) {
	parsed := value.Float()
	switch value.Type {
	case gjson.Number:
	case gjson.String:
		var errParse error
		if parsed, errParse = strconv.ParseFloat(strings.TrimSpace(value.String()), 64); errParse != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		return 0, false
	}
	return parsed, true
}

// dynaTime reads an RFC 3339 string or a Unix time in seconds or milliseconds.
// A value it cannot read is the zero time.
func dynaTime(value gjson.Result) time.Time {
	switch value.Type {
	case gjson.String:
		parsed, errParse := time.Parse(time.RFC3339Nano, value.String())
		if errParse != nil {
			return time.Time{}
		}
		return parsed
	case gjson.Number:
		switch at := value.Int(); {
		case at <= 0:
			return time.Time{}
		case at >= 100_000_000_000:
			return time.UnixMilli(at)
		default:
			return time.Unix(at, 0)
		}
	default:
		return time.Time{}
	}
}

func dynaAccessToken(auth *coreauth.Auth) string {
	return stringValue(auth.Metadata, "access_token")
}

func dynaAPIKey(auth *coreauth.Auth) string {
	if auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["api_key"])
}

func dynaBearer(token string) http.Header {
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+token)
	header.Set("Accept", "application/json")
	return header
}

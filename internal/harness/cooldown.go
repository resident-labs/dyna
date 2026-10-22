package harness

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/profile"
)

// ModelCooldownDuration is the retry delay after a provider rejects a model.
const ModelCooldownDuration = time.Hour

var cooldownNow = time.Now

type modelKey struct {
	profile string
	model   string
}

var modelCooldowns = struct {
	sync.Mutex
	failures map[modelKey]time.Time
}{failures: make(map[modelKey]time.Time)}

// ResetModelCooldownsForTest clears process-local model failures.
func ResetModelCooldownsForTest() {
	modelCooldowns.Lock()
	defer modelCooldowns.Unlock()
	modelCooldowns.failures = make(map[modelKey]time.Time)
}

func RecordModelFailure(profileName, model string, now time.Time) {
	modelCooldowns.Lock()
	defer modelCooldowns.Unlock()
	key := modelKey{profileName, model}
	at, exists := modelCooldowns.failures[key]
	if !exists || now.After(at) {
		modelCooldowns.failures[key] = now
	}
}

func ClearModelFailure(profileName, model string) {
	modelCooldowns.Lock()
	defer modelCooldowns.Unlock()
	delete(modelCooldowns.failures, modelKey{profileName, model})
}

func IsModelCoolingDown(profileName, model string, now time.Time) bool {
	modelCooldowns.Lock()
	defer modelCooldowns.Unlock()
	at, ok := modelCooldowns.failures[modelKey{profileName, model}]
	return ok && now.Before(at.Add(ModelCooldownDuration))
}

// SelectHealthyModel preserves profile order among healthy models. If all
// models are cooling down, it retries them in order of oldest failure first.
func SelectHealthyModel(p profile.Profile, now time.Time) (string, []string) {
	models := p.EffectiveModels()
	if len(models) <= 1 {
		return p.Model, models
	}
	modelCooldowns.Lock()
	defer modelCooldowns.Unlock()
	var healthy, cooling []string
	for _, model := range models {
		at, ok := modelCooldowns.failures[modelKey{p.Name, model}]
		if !ok || !now.Before(at.Add(ModelCooldownDuration)) {
			healthy = append(healthy, model)
		} else {
			cooling = append(cooling, model)
		}
	}
	if len(healthy) > 0 {
		return healthy[0], healthy
	}
	sort.SliceStable(cooling, func(i, j int) bool {
		return modelCooldowns.failures[modelKey{p.Name, cooling[i]}].Before(modelCooldowns.failures[modelKey{p.Name, cooling[j]}])
	})
	return cooling[0], cooling
}

// notReady marks a turn that failed before the model ran, such as a signed-out
// account or a missing CLI. The profile cannot work until someone fixes it.
// A canceled turn says nothing about the profile.
func notReady(ctx context.Context, p profile.Profile, err error) error {
	if ctx.Err() != nil {
		return err
	}
	return fmt.Errorf("worker %s %s %w", p.Name, notReadyText, err)
}

const notReadyText = "runtime is not ready:"

func IsNotReady(text string) bool { return strings.Contains(text, notReadyText) }

// IsProviderFailure matches provider quota, rate limit, auth, and 5xx errors.
func IsProviderFailure(text string) bool {
	text = strings.ToLower(text)
	for _, term := range []string{
		"quota", "credit balance", "insufficient credit", "out of credit", "429", "rate limit", "rate_limit", "overloaded", "at capacity", "usage limit", "usage_limit", "session limit", "weekly limit",
		"authentication_error", "invalid api key", "invalid_api_key", "not logged in",
		"internal server error", "bad gateway", "service unavailable",
	} {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

// IsCapacityFailure matches a provider that is busy for now, not one that
// rejects the account: the same request can succeed a few minutes later.
func IsCapacityFailure(text string) bool {
	text = strings.ToLower(text)
	for _, term := range []string{"at capacity", "overloaded", "service unavailable"} {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

// IsUsageLimit matches a quota, credit or rate limit. Waiting a few minutes
// does not lift it, so the session needs another model.
func IsUsageLimit(text string) bool {
	text = strings.ToLower(text)
	for _, term := range []string{"quota", "credit", "429", "rate limit", "rate_limit", "usage limit", "usage_limit", "session limit", "weekly limit"} {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

// classifyFailure tells the owner what kind of provider failure ended a run and
// what to do. Other errors, including unclassified provider ones, pass as they are.
func classifyFailure(err error, capacityRetries int) error {
	switch {
	case err == nil:
		return nil
	case IsCapacityFailure(err.Error()):
		return fmt.Errorf("%w; the model stayed at capacity after %d retries with backoff (%v); ask root to investigate or move the session to another model", err, capacityRetries, capacityBackoff[:min(capacityRetries, len(capacityBackoff))])
	case IsUsageLimit(err.Error()):
		return fmt.Errorf("%w; usage limit reached, so this session needs another model", err)
	}
	return err
}

var resetAt = regexp.MustCompile(`(?i)resets? at ([0-9T:.+Z-]+)`)

var resetIn = regexp.MustCompile(`(?i)resets? in ([0-9][0-9hms]*)`)

// UsageResetAt returns the provider's stated reset time, or zero when unknown.
// It reads both the absolute form, "resets at 2030-01-02T22:19:00Z", and the
// relative form, "resets in 152h54m19s".
func UsageResetAt(text string, at time.Time) time.Time {
	if m := resetAt.FindStringSubmatch(text); m != nil {
		if reset, err := time.Parse(time.RFC3339Nano, m[1]); err == nil && reset.After(at) {
			return reset
		}
	}
	if m := resetIn.FindStringSubmatch(text); m != nil {
		if d, err := time.ParseDuration(m[1]); err == nil && d > 0 {
			return at.Add(d)
		}
	}
	return time.Time{}
}

// UnavailableUntil keeps a profile out for one hour after a provider failure,
// or until the reset time that a quota error gives, such as
// "Resets in 152h54m19s".
func UnavailableUntil(text string, at time.Time) time.Time {
	wait := time.Hour
	if reset := UsageResetAt(text, at); !reset.IsZero() {
		if d := reset.Sub(at); d > wait {
			wait = d
		}
	}
	return at.Add(wait)
}

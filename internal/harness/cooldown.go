package harness

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/harness/modelcooldown"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/providerfailure"
)

// ModelCooldownDuration is the retry delay after a provider rejects a model.
const ModelCooldownDuration = time.Hour

var cooldownNow = time.Now

// modelCooldowns is the durable record of model failures. Its zero value uses
// the owner's data folder, so a turn runner in any process sees the rows that
// another process wrote, including the detached runner of the next turn.
var modelCooldowns modelcooldown.Store

// RecordModelFailure keeps the newest failure time of one model of one profile.
// Model bookkeeping is best effort: a storage error is logged and the turn
// still runs, as with the harness health record.
func RecordModelFailure(profileName, model string, now time.Time) {
	if err := modelCooldowns.Record(context.Background(), profileName, model, now); err != nil {
		log.Printf("record model cooldown %s %s: %v", profileName, model, err)
	}
}

// ClearModelFailure deletes a model's failure, because a turn on it succeeded.
func ClearModelFailure(profileName, model string) {
	if err := modelCooldowns.Clear(context.Background(), profileName, model); err != nil {
		log.Printf("clear model cooldown %s %s: %v", profileName, model, err)
	}
}

// IsModelCoolingDown reports whether the model failed less than the cooldown
// window ago. An unreadable store reports no cooldown, so a turn still runs.
func IsModelCoolingDown(profileName, model string, now time.Time) bool {
	failures, err := modelCooldowns.Failures(context.Background(), profileName)
	if err != nil {
		log.Printf("read model cooldowns of profile %s: %v", profileName, err)
		return false
	}
	at, ok := failures[model]
	return ok && now.Before(at.Add(ModelCooldownDuration))
}

// SelectHealthyModel preserves profile order among healthy models. If all
// models are cooling down, it retries them in order of oldest failure first.
// It reads the durable cooldowns, so a failure another process recorded still
// counts. An unreadable store keeps profile order, so the turn still starts.
func SelectHealthyModel(p profile.Profile, now time.Time) (string, []string) {
	models := p.EffectiveModels()
	if len(models) <= 1 {
		return p.Model, models
	}
	failures, err := modelCooldowns.Failures(context.Background(), p.Name)
	if err != nil {
		log.Printf("read model cooldowns of profile %s: %v", p.Name, err)
		return models[0], models
	}
	var healthy, cooling []string
	for _, model := range models {
		at, ok := failures[model]
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
		return failures[cooling[i]].Before(failures[cooling[j]])
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

// classifyFailure tells the owner what kind of provider failure ended a run and
// what to do. Only a reason a trusted boundary named selects the advice; other
// errors, including unclassified ones, pass as they are.
func classifyFailure(err error, capacityRetries int) error {
	reason, ok := providerfailure.ReasonOf(err)
	switch {
	case err == nil || !ok:
		return err
	case reason == providerfailure.Capacity:
		return fmt.Errorf("%w; the model stayed at capacity after %d retries with backoff (%v); ask root to investigate or move the session to another model", err, capacityRetries, capacityBackoff[:min(capacityRetries, len(capacityBackoff))])
	case reason == providerfailure.Quota:
		return fmt.Errorf("%w; usage limit reached, so this session needs another model", err)
	case reason == providerfailure.Auth:
		return fmt.Errorf("%w; the provider rejected the account's sign-in, so another model cannot help and the account needs a new sign-in", err)
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

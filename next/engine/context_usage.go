package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

// usageChars is the section's budget of 80 tokens.
const usageChars = 320

// usageLines gives one line per account the session's room may use. Accounts routing would use come first, so the
// budget keeps them and "N more" counts the rest. A reading holds 10% steps and a pace word, never a time, and the
// reading itself changes at most once per usage cache age, so a line changes rarely, not every turn.
func usageLines(accounts []store.ContextAccount, readings []contract.AccountUsage, now time.Time) []string {
	byID := make(map[contract.AccountID]contract.AccountUsage, len(readings))
	for _, reading := range readings {
		byID[reading.AccountID] = reading
	}
	var active, idle []string
	for _, account := range accounts {
		reading, ok := byID[account.ID]
		if !ok || !account.Usable {
			continue
		}
		var windows []string
		for _, window := range shownWindows(reading.Windows) {
			windows = append(windows, usageWindow(window, now))
		}
		if len(windows) == 0 {
			continue
		}
		line := fmt.Sprintf("%s %d: %s", model.ProviderName(account.Provider), account.Number, strings.Join(windows, ", "))
		if reading.Active {
			active = append(active, line)
		} else {
			idle = append(idle, line+" (idle)")
		}
	}
	return append(active, idle...)
}

// shownWindows keeps a model's own window only when it adds something, as Home does (usage.ts).
func shownWindows(windows []contract.UsageWindow) []contract.UsageWindow {
	var shown []contract.UsageWindow
	for _, window := range windows {
		repeats := false
		for _, other := range windows {
			repeats = repeats || (other.Model == "" && other.Seconds == window.Seconds && other.ResetsAt.Equal(window.ResetsAt) && usedStep(other) == usedStep(window))
		}
		if window.Model == "" || (usedStep(window) > 0 && !repeats) {
			shown = append(shown, window)
		}
	}
	return shown
}

// usedStep rounds down, so a window reads 100% only when it is spent.
func usedStep(window contract.UsageWindow) int {
	return min(100, max(0, int(window.UsedPercent)/10*10))
}

func usageWindow(window contract.UsageWindow, now time.Time) string {
	name := map[contract.UsageWindowKind]string{contract.UsageWindowFiveHour: "5-hour", contract.UsageWindowWeekly: "weekly", contract.UsageWindowMonthly: "monthly"}[window.Kind]
	if name == "" {
		name = "usage"
	}
	if window.Model != "" {
		name = window.Model + " " + name
	}
	text := fmt.Sprintf("%s %d%%", name, usedStep(window))
	if pace := usagePace(window, now); pace != "" {
		text += " " + pace
	}
	return text
}

// usagePace compares usage with the part of the window that has passed. A window whose reset is unknown or already
// past has no such part, so it has no pace.
func usagePace(window contract.UsageWindow, now time.Time) string {
	length := time.Duration(window.Seconds) * time.Second
	left := window.ResetsAt.Sub(now)
	if length <= 0 || window.ResetsAt.IsZero() || left <= 0 {
		return ""
	}
	elapsed := 100 * (1 - min(left, length).Seconds()/length.Seconds())
	switch gap := window.UsedPercent - elapsed; {
	case gap > 10:
		return "ahead"
	case gap < -10:
		return "under"
	}
	return "on"
}

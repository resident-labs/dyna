package engine

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type fixedUsage struct {
	readings []contract.AccountUsage
	calls    int
}

func (u *fixedUsage) Latest() []contract.AccountUsage { u.calls++; return u.readings }

// usageWindowAt is a window with elapsed percent of its length passed.
func usageWindowAt(kind contract.UsageWindowKind, used, elapsed float64, now time.Time) contract.UsageWindow {
	const length = int64(18000)
	left := time.Duration(float64(length)*(100-elapsed)/100) * time.Second
	return contract.UsageWindow{Kind: kind, UsedPercent: used, Seconds: length, ResetsAt: now.Add(left)}
}

func TestUsageWindowIsATenPercentStepWithAPace(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tc := range []struct {
		used, elapsed float64
		want          string
	}{
		{0, 0, "5-hour 0% on"},
		{59, 50, "5-hour 50% on"},
		{60, 50, "5-hour 60% on"},      // exactly 10 points above is still on
		{60.5, 50, "5-hour 60% ahead"}, // over 10 points above
		{39.5, 50, "5-hour 30% under"}, // over 10 points below
		{40, 50, "5-hour 40% on"},      // exactly 10 points below is still on
		{99.9, 90, "5-hour 90% on"},    // rounds down: only a spent window reads 100%
		{100, 95, "5-hour 100% on"},
		{120, 95, "5-hour 100% ahead"},
	} {
		if got := usageWindow(usageWindowAt(contract.UsageWindowFiveHour, tc.used, tc.elapsed, now), now); got != tc.want {
			t.Errorf("used %v elapsed %v: %q, want %q", tc.used, tc.elapsed, got, tc.want)
		}
	}
	noReset := contract.UsageWindow{Kind: contract.UsageWindowWeekly, UsedPercent: 25, Seconds: 604800}
	if got := usageWindow(noReset, now); got != "weekly 20%" {
		t.Errorf("no reset: %q", got)
	}
	past := contract.UsageWindow{Kind: contract.UsageWindowMonthly, UsedPercent: 25, Seconds: 2592000, ResetsAt: now.Add(-time.Minute)}
	if got := usageWindow(past, now); got != "monthly 20%" {
		t.Errorf("past reset: %q", got)
	}
}

func TestUsageLinesNameProvidersAndKeepActiveAccountsFirst(t *testing.T) {
	now := time.Unix(1000, 0)
	accounts := []store.ContextAccount{
		{ID: "a", Provider: contract.ProviderAnthropic, Number: 1, Usable: true},
		{ID: "b", Provider: contract.ProviderAnthropic, Number: 2, Usable: true},
		{ID: "c", Provider: contract.ProviderOpenAI, Number: 1, Usable: true},
		{ID: "d", Provider: contract.ProviderKimi, Number: 1, Usable: false},
		{ID: "e", Provider: "custom-acme", Number: 1, Usable: true},
		{ID: "gone", Provider: contract.ProviderZAI, Number: 1, Usable: true},
	}
	window := usageWindowAt(contract.UsageWindowFiveHour, 50, 50, now)
	readings := []contract.AccountUsage{
		{AccountID: "a", Label: "owner@example.com", Windows: []contract.UsageWindow{window}},
		{AccountID: "b", Active: true, Windows: []contract.UsageWindow{window}},
		{AccountID: "c", Active: true, Windows: []contract.UsageWindow{window}},
		{AccountID: "d", Active: true, Windows: []contract.UsageWindow{window}},
		{AccountID: "e", Active: true, Windows: []contract.UsageWindow{window}},
		{AccountID: "unbound", Active: true, Windows: []contract.UsageWindow{window}},
	}
	got := strings.Join(usageLines(accounts, readings, now), "\n")
	want := "Claude 2: 5-hour 50% on\nChatGPT 1: 5-hour 50% on\nacme 1: 5-hour 50% on\nClaude 1: 5-hour 50% on (idle)"
	if got != want {
		t.Fatalf("lines:\n%s\nwant:\n%s", got, want)
	}
}

func TestUsageLeavesOutAModelWindowThatAddsNothing(t *testing.T) {
	now := time.Unix(1000, 0)
	weekly := contract.UsageWindow{Kind: contract.UsageWindowWeekly, UsedPercent: 40, Seconds: 604800, ResetsAt: now.Add(time.Hour)}
	copied := weekly
	copied.Model = "Code"
	unused := weekly
	unused.Model, unused.UsedPercent = "Opus", 2
	own := weekly
	own.Model, own.UsedPercent = "Sonnet", 75
	got := shownWindows([]contract.UsageWindow{weekly, copied, unused, own})
	if len(got) != 2 || got[0].Model != "" || got[1].Model != "Sonnet" {
		t.Fatalf("windows: %+v", got)
	}
}

// usageRoom seeds accounts the snapshot's store query reads.
func usageRoom(t *testing.T, s *Service, ctx context.Context, n int) {
	t.Helper()
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		for i := range n {
			if _, err := tx.ExecContext(ctx, `INSERT INTO accounts(id,proxy_id,provider,label) VALUES(?,?,'anthropic','owner@example.com')`, fmt.Sprintf("a%02d", i), fmt.Sprintf("p%02d", i)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func usageSection(t *testing.T, s *Service, ctx context.Context, session contract.SessionID, section contract.ContextSectionKey) string {
	t.Helper()
	snapshot, problem := s.ContextSnapshot(ctx, asSession(session), contract.ContextSnapshotRequest{Section: section})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, found := range snapshot.Sections {
		if found.Key == contract.ContextUsage {
			return found.Text
		}
	}
	return ""
}

func TestUsageSectionHoldsItsBudgetAndKeepsActiveAccounts(t *testing.T) {
	s, _, session, ctx := setup(t)
	usageRoom(t, s, ctx, 12)
	var readings []contract.AccountUsage
	for i := range 12 {
		windows := []contract.UsageWindow{usageWindowAt(contract.UsageWindowFiveHour, 55, 50, s.now()), usageWindowAt(contract.UsageWindowWeekly, 35, 50, s.now())}
		readings = append(readings, contract.AccountUsage{AccountID: contract.AccountID(fmt.Sprintf("a%02d", i)), Label: "owner@example.com", Windows: windows, Active: i >= 9})
	}
	s.SetUsage(&fixedUsage{readings: readings})
	text := usageSection(t, s, ctx, session, "")
	if n := len([]rune(text)); n > usageChars {
		t.Fatalf("%d characters: %s", n, text)
	}
	lines := strings.Split(text, "\n")
	if !strings.HasSuffix(text, " more: context show") {
		t.Fatalf("no overflow line: %s", text)
	}
	for i, want := range []string{"Claude 10:", "Claude 11:", "Claude 12:"} {
		if !strings.HasPrefix(lines[i], want) {
			t.Fatalf("line %d: %q", i, lines[i])
		}
	}
	if strings.Contains(text, "example.com") {
		t.Fatalf("label in section: %s", text)
	}
	// context show gives every account.
	if full := usageSection(t, s, ctx, session, contract.ContextUsage); strings.Count(full, "\n") != 11 {
		t.Fatalf("full section: %s", full)
	}
}

func TestUsageSectionDoesNotChangeBetweenTurns(t *testing.T) {
	s, _, session, ctx := setup(t)
	usageRoom(t, s, ctx, 2)
	base := time.Unix(1000, 0)
	reading := func(five, weekly float64) []contract.AccountUsage {
		return []contract.AccountUsage{{AccountID: "a00", Active: true, Windows: []contract.UsageWindow{
			{Kind: contract.UsageWindowFiveHour, UsedPercent: five, Seconds: 18000, ResetsAt: base.Add(4*time.Hour + 15*time.Minute)},
			{Kind: contract.UsageWindowWeekly, UsedPercent: weekly, Seconds: 604800, ResetsAt: base.Add(7 * 24 * time.Hour)},
		}}}
	}
	usage := &fixedUsage{readings: reading(11, 21)}
	s.SetUsage(usage)
	first := usageSection(t, s, ctx, session, "")
	if first != "Claude 1: 5-hour 10% on, weekly 20% ahead" {
		t.Fatalf("first: %q", first)
	}
	if regexp.MustCompile(`\d{1,2}:\d{2}|\d{4}-\d{2}|\d+ ?(min|sec)`).MatchString(first) {
		t.Fatalf("a timestamp or duration: %q", first)
	}
	// Forty turns over twenty minutes, with usage moving inside its steps: nothing to send.
	for i := range 40 {
		at := base.Add(time.Duration(i) * 30 * time.Second)
		s.now = func() time.Time { return at }
		usage.readings = reading(11+float64(i)/10, 21+float64(i)/10)
		if got := usageSection(t, s, ctx, session, ""); got != first {
			t.Fatalf("turn %d changed it: %q", i, got)
		}
	}
	usage.readings = reading(20, 21)
	if got := usageSection(t, s, ctx, session, ""); got != "Claude 1: 5-hour 20% on, weekly 20% ahead" {
		t.Fatalf("next step: %q", got)
	}
}

func TestUsageSectionShowsOnlyTheRoomsAccounts(t *testing.T) {
	s, room, session, ctx := setup(t)
	usageRoom(t, s, ctx, 3)
	window := contract.UsageWindow{Kind: contract.UsageWindowFiveHour, UsedPercent: 30}
	s.SetUsage(&fixedUsage{readings: []contract.AccountUsage{{AccountID: "a00", Active: true, Windows: []contract.UsageWindow{window}}, {AccountID: "a01", Active: true, Windows: []contract.UsageWindow{window}}, {AccountID: "a02", Active: true, Windows: []contract.UsageWindow{window}}}})
	if err := s.store.CreateRoom(ctx, "elsewhere", "elsewhere", s.now()); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetAccountRooms(ctx, "a00", []contract.RoomID{"elsewhere"}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetAccountRooms(ctx, "a01", []contract.RoomID{room}); err != nil {
		t.Fatal(err)
	}
	// The limit does not renumber: Claude 2 and 3 keep their names.
	if got := usageSection(t, s, ctx, session, ""); got != "Claude 2: 5-hour 30%\nClaude 3: 5-hour 30%" {
		t.Fatalf("section: %q", got)
	}
}

func TestSnapshotHasNoUsageSectionWithoutAReading(t *testing.T) {
	s, _, session, ctx := setup(t)
	usageRoom(t, s, ctx, 1)
	if got := usageSection(t, s, ctx, session, ""); got != "" {
		t.Fatalf("section without a reader: %q", got)
	}
	s.SetUsage(&fixedUsage{})
	if got := usageSection(t, s, ctx, session, contract.ContextUsage); got != "" {
		t.Fatalf("section without a reading: %q", got)
	}
}

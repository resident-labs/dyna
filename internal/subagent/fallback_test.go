package subagent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
)

// limitProfile fails every turn on a usage limit, like a harness whose account
// has no quota left and no same-harness account to move to.
func limitProfile(name, fallback string) profile.Profile {
	return profile.Profile{
		Name: name, Harness: profile.HarnessCustom, Model: "only", Fallback: fallback,
		Taste: 5, Intelligence: 5, Cost: 5,
		Command: []string{"/bin/sh", "-c", `/bin/cat >/dev/null; printf 'quota exhausted; resets in 2h' >&2; exit 1`},
	}
}

// A usage limit with no account left moves the session to its fallback profile
// with the shared packet, and the turn continues there.
func TestUsageLimitFallbackMovesSessionAndContinues(t *testing.T) {
	first := mockProfile("limit")
	first.Fallback = "next"
	e := newTestEnv(t, first, mockProfile("next"))
	e.m.TransferLaunch = func(*Manager, string, string) error { return nil }
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	info, _ := e.start("RESPOND: continue the decoder task\nUSAGE_LIMIT: resets in 2h", func(o *StartOptions) { o.Profile = "limit" })
	if err := e.m.RunTurn(e.ctx, info.ID, 1, held.lock); err == nil {
		t.Fatal("a usage-limit turn reported success")
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Transfer == nil || meta.Transfer.Profile != "next" || !meta.Transfer.Active() {
		t.Fatalf("no fallback transfer saved: %+v", meta.Transfer)
	}
	if !slices.Equal(meta.FallbackPath, []string{"limit", "next"}) {
		t.Fatalf("fallback path = %v", meta.FallbackPath)
	}
	if notice := meta.Transfer.Notice; !strings.Contains(notice, "Moved from limit to next") || !strings.Contains(notice, "resets ") || !strings.Contains(notice, "will not move it back automatically") {
		t.Fatalf("notice = %q", notice)
	}
	if meta.Transfer.WaitFor != 0 || meta.Transfer.Posted {
		t.Fatalf("fallback asked the failed model for a handoff: %+v", meta.Transfer)
	}
	state := *meta.Transfer
	// waiting -> stopping (no second ask), then stopping -> starting, then the
	// replacement turn is created.
	advance(t, e, state, 3)
	if got, _ := e.m.TransferStatus(info.ID); got.Phase != "starting" {
		t.Fatalf("phase = %s", got.Phase)
	}
	if err := e.m.RunTurn(e.ctx, info.ID, 2, held.lock); err != nil {
		t.Fatalf("replacement turn failed: %v", err)
	}
	meta, err = e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Profile != "next" || meta.Model != "" || turn.Profile != "next" || turn.Status != TurnOK {
		t.Fatalf("session did not continue on the fallback: meta=%+v turn=%+v", meta, turn)
	}
	for _, want := range []string{"[DYNA HANDOFF PACKET]", "Moved because: usage limit on limit", "dyna subagent output " + info.ID + " --turn 1 --transcript"} {
		if !strings.Contains(turn.Prompt, want) {
			t.Fatalf("replacement prompt lacks %q:\n%s", want, turn.Prompt)
		}
	}
	if strings.Contains(turn.Prompt, "USAGE_LIMIT") {
		t.Fatal("replacement prompt copied the failed turn")
	}
	// One notice in the session's own DM, and nothing sent to root.
	msgs, _, err := e.m.rooms().ReadAll(info.RoomID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var notices []room.Message
	for _, msg := range msgs {
		if msg.Kind == "account" {
			notices = append(notices, msg)
		}
	}
	if len(notices) != 1 {
		t.Fatalf("account notices = %d, want 1: %+v", len(notices), notices)
	}
	if !slices.Contains(notices[0].To, info.Address()) || slices.Contains(notices[0].To, info.Room+":"+RoleRoot) {
		t.Fatalf("notice recipients = %v", notices[0].To)
	}
	if !strings.Contains(notices[0].Text, "Moved from limit to next") {
		t.Fatalf("notice text = %q", notices[0].Text)
	}
}

// A cyclic chain does not loop: the second limit finds no profile the session
// has not already left, so nothing moves.
func TestFallbackChainDoesNotLoop(t *testing.T) {
	e := newTestEnv(t, limitProfile("one", "two"), limitProfile("two", "one"))
	e.m.TransferLaunch = func(*Manager, string, string) error { return nil }
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	info, _ := e.start("RESPOND: work", func(o *StartOptions) { o.Profile = "one" })
	if err := e.m.RunTurn(e.ctx, info.ID, 1, held.lock); err == nil {
		t.Fatal("a usage-limit turn reported success")
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	if meta.Transfer == nil || meta.Transfer.Profile != "two" {
		t.Fatalf("first fallback missing: %+v", meta.Transfer)
	}
	firstToken := meta.Transfer.ID
	state := *meta.Transfer
	advance(t, e, state, 3)
	if err := e.m.RunTurn(e.ctx, info.ID, 2, held.lock); err == nil {
		t.Fatal("the replacement turn hit no limit")
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Transfer.ID != firstToken || meta.Transfer.Profile != "two" {
		t.Fatalf("a cyclic chain moved the session again: %+v", meta.Transfer)
	}
	if !slices.Equal(meta.FallbackPath, []string{"one", "two"}) {
		t.Fatalf("fallback path = %v", meta.FallbackPath)
	}
}

// An exhausted fallback continues the chain: one -> two -> three, with the
// second limit reached while the first transfer is still starting. The first
// coordinator must neither block the third target nor restore the first
// profile.
func TestFallbackChainContinuesAfterASecondLimit(t *testing.T) {
	e := newTestEnv(t, limitProfile("one", "two"), limitProfile("two", "three"), mockProfile("three"))
	e.m.TransferLaunch = func(*Manager, string, string) error { return nil }
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	info, _ := e.start("RESPOND: work", func(o *StartOptions) { o.Profile = "one" })
	if err := e.m.RunTurn(e.ctx, info.ID, 1, held.lock); err == nil {
		t.Fatal("turn 1 reported success")
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Transfer == nil || meta.Transfer.Profile != "two" {
		t.Fatalf("first move missing: %+v", meta.Transfer)
	}
	firstToken := meta.Transfer.ID
	first := *meta.Transfer
	advance(t, e, first, 3)
	if got, _ := e.m.TransferStatus(info.ID); got.Phase != "starting" {
		t.Fatalf("first transfer phase = %s", got.Phase)
	}
	if err := e.m.RunTurn(e.ctx, info.ID, 2, held.lock); err == nil {
		t.Fatal("the fallback turn hit no limit")
	}
	meta, err = e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Transfer == nil || meta.Transfer.ID == firstToken || meta.Transfer.Profile != "three" {
		t.Fatalf("chain did not continue: %+v", meta.Transfer)
	}
	if !slices.Equal(meta.FallbackPath, []string{"one", "two", "three"}) {
		t.Fatalf("fallback path = %v", meta.FallbackPath)
	}
	if meta.Profile != "two" {
		t.Fatalf("second move changed the profile before starting: %s", meta.Profile)
	}
	// The settled move is finished, so its own coordinator has nothing to do.
	if done, err := e.m.advanceTransfer(e.ctx, info.ID, firstToken); err != nil || !done {
		t.Fatalf("old coordinator still owns the session: done=%v err=%v", done, err)
	}
	if meta, _ = e.m.Store.ReadMeta(info.ID); meta.Profile != "two" {
		t.Fatalf("old coordinator restored profile %s", meta.Profile)
	}
	// The third profile accepts the work.
	third := *meta.Transfer
	advance(t, e, third, 3)
	if err := e.m.RunTurn(e.ctx, info.ID, 3, held.lock); err != nil {
		t.Fatalf("turn 3 failed: %v", err)
	}
	meta, err = e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Profile != "three" || turn.Profile != "three" || turn.Status != TurnOK {
		t.Fatalf("chain ended wrong: meta=%+v turn=%+v", meta, turn)
	}
}

// turnLocks hands each spawned turn and its lock to the test, so a real
// coordinator's spawn is an event rather than a polled field.
type turnLocks struct{ ch chan spawnedTurn }

type spawnedTurn struct {
	turn int
	lock *os.File
}

func (s *turnLocks) Spawn(_ *Manager, _ string, turn int, lock *os.File) error {
	s.ch <- spawnedTurn{turn, lock}
	return nil
}

// The coordinator of a superseding fallback must really start. The previous
// coordinator is still registered and holds the session's transfer lock when
// the second move is saved, so a worker key that names only the session would
// drop the new launch and leave the move waiting forever.
func TestFallbackChainSupersedingTransferGetsItsCoordinator(t *testing.T) {
	e := newTestEnv(t, limitProfile("one", "two"), limitProfile("two", "three"), mockProfile("three"))
	coordinatorWorkers(t, e)
	locks := &turnLocks{ch: make(chan spawnedTurn, 4)}
	e.m.Spawner = locks
	// Every coordinator parks on one release event, so both the old and the new
	// worker are alive at the moment of the supersede. Parked reports each
	// coordinator that reaches a wait point; nothing here polls or sleeps.
	parked := make(chan struct{}, 4)
	release := make(chan struct{})
	e.m.TransferSleep = func(ctx context.Context, _ time.Duration) error {
		select {
		case parked <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	waitParked := func(what string) {
		t.Helper()
		select {
		case <-parked:
		case <-time.After(coordinatorFailureBound):
			t.Fatalf("%s never reached a coordinator wait point", what)
		}
	}
	nextTurn := func(want int) *os.File {
		t.Helper()
		select {
		case got := <-locks.ch:
			if got.turn != want {
				t.Fatalf("spawned turn %d, want turn %d", got.turn, want)
			}
			return got.lock
		case <-time.After(coordinatorFailureBound):
			t.Fatalf("turn %d never started", want)
			return nil
		}
	}
	info, _ := e.start("RESPOND: work", func(o *StartOptions) { o.Profile = "one" })
	if err := e.m.RunTurn(e.ctx, info.ID, 1, nextTurn(1)); err == nil {
		t.Fatal("turn 1 reported success")
	}
	// The first coordinator has created turn 2 and waits, still holding the
	// transfer lock the superseding move needs.
	waitParked("the first coordinator")
	if err := e.m.RunTurn(e.ctx, info.ID, 2, nextTurn(2)); err == nil {
		t.Fatal("the fallback turn hit no limit")
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Transfer == nil || meta.Transfer.Profile != "three" {
		t.Fatalf("second move missing: %+v", meta.Transfer)
	}
	// The superseding transfer needs a live coordinator while the first one is
	// still parked. With a session-only worker key it never gets one.
	waitParked("the superseding coordinator")
	close(release)
	if err := e.m.RunTurn(e.ctx, info.ID, 3, nextTurn(3)); err != nil {
		t.Fatalf("turn 3 failed: %v", err)
	}
	// Joining the workers proves the third move advanced and finished, and that
	// the stale coordinator left without restoring the first profile.
	waitCoordinators(t, e)
	got, err := e.m.TransferStatus(info.ID)
	if err != nil || got == nil || got.Phase != "completed" {
		t.Fatalf("the superseding transfer did not finish: %+v %v", got, err)
	}
	meta, err = e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := e.m.Store.ReadTurn(info.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Profile != "three" || turn.Status != TurnOK {
		t.Fatalf("chain ended wrong: meta=%+v turn=%+v", meta, turn)
	}
}

func TestFallbackTargetRefusesLoopsAndMissingProfiles(t *testing.T) {
	ps := &profile.Store{Profiles: []profile.Profile{
		{Name: "one", Fallback: "two"},
		{Name: "two"},
		{Name: "off", Fallback: "two", Disabled: true},
		{Name: "to-off", Fallback: "off"},
		{Name: "to-gone", Fallback: "gone"},
		{Name: "self", Fallback: "self"},
	}}
	for _, tc := range []struct {
		name string
		meta Meta
		want string
	}{
		{"next in chain", Meta{Profile: "one"}, "two"},
		{"no fallback", Meta{Profile: "two"}, ""},
		{"disabled target", Meta{Profile: "to-off"}, ""},
		{"missing target", Meta{Profile: "to-gone"}, ""},
		{"already left", Meta{Profile: "one", FallbackPath: []string{"one", "two"}}, ""},
		{"chain too long", Meta{Profile: "one", FallbackPath: []string{"a", "b", "c", "d", "e", "f", "g", "h"}}, ""},
		{"unknown profile", Meta{Profile: "gone"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := fallbackTarget(tc.meta, ps)
			if tc.want == "" {
				if ok {
					t.Fatalf("fallbackTarget accepted %q", got.Name)
				}
				return
			}
			if !ok || got.Name != tc.want {
				t.Fatalf("fallbackTarget = %q, %v; want %q", got.Name, ok, tc.want)
			}
		})
	}
	// A profile that names itself is refused before any turn runs.
	self := profile.Profile{Name: "self", Harness: profile.HarnessMock, Fallback: "self", Taste: 5, Intelligence: 5, Cost: 5}
	if err := self.Validate(); err == nil {
		t.Fatal("validated a profile that falls back to itself")
	}
}

func TestFallbackNoticeNamesResetOnlyWhenGiven(t *testing.T) {
	now := time.Date(2026, 5, 4, 20, 19, 0, 0, time.UTC)
	got := fallbackNotice("work:lead", "ses_1", "glm", "deepseek", "session limit; resets in 2h", now)
	if !strings.Contains(got, "Moved from glm to deepseek: usage limit, resets 22:19.") {
		t.Fatalf("notice = %q", got)
	}
	if !strings.Contains(got, "dyna subagent transfer work:lead --profile glm") {
		t.Fatalf("notice does not say how to move back: %q", got)
	}
	if got := fallbackNotice("work:lead", "ses_1", "glm", "deepseek", "quota exhausted", now); strings.Contains(got, "resets") {
		t.Fatalf("notice invented a reset time: %q", got)
	}
	// A session name may be very long; the notice keeps the ID handle so the
	// post stays within one message.
	if got := fallbackNotice("work:"+strings.Repeat("n", 11000), "ses_1", "glm", "deepseek", "quota exhausted", now); !strings.Contains(got, "dyna subagent transfer ses_1 --profile glm") || len(got) > 1000 {
		t.Fatalf("long name did not fall back to the session ID: %d bytes", len(got))
	}
}

// The fallback path stores no secrets and leaves the worktree and PR record
// untouched; the replacement continues in the same directory.
func TestFallbackKeepsDirectoryAndTrackedPR(t *testing.T) {
	e := newTestEnv(t, limitProfile("one", "two"), limitProfile("two", ""))
	e.m.TransferLaunch = func(*Manager, string, string) error { return nil }
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	info, _ := e.start("RESPOND: work", func(o *StartOptions) { o.Profile = "one" })
	writePRLink(t, info, "author", "open", true)
	if err := e.m.RunTurn(e.ctx, info.ID, 1, held.lock); err == nil {
		t.Fatal("a usage-limit turn reported success")
	}
	meta, _ := e.m.Store.ReadMeta(info.ID)
	state := *meta.Transfer
	advance(t, e, state, 2)
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Dir != info.Dir {
		t.Fatalf("worktree changed: %s -> %s", info.Dir, meta.Dir)
	}
	links, err := e.m.TrackedPRs(info.ID)
	if err != nil || len(links) != 1 || !links[0].Active() {
		t.Fatalf("PR ownership lost: %+v %v", links, err)
	}
	if _, err := os.Stat(filepath.Join(meta.Dir)); err != nil {
		t.Fatalf("worktree unavailable: %v", err)
	}
}

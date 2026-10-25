package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/providerfailure"
)

const limitText = "You've hit your session limit · resets in 3h"

// quotaFailure is the same limit as a trusted boundary reports it: a typed
// quota reason, so recovery can route on it.
func quotaFailure() error {
	return providerfailure.Tag(providerfailure.Quota, errors.New(limitText))
}

func failoverWith(t *testing.T, fake func(context.Context, profile.Profile, string, string, error) (*AccountSwitch, error)) {
	t.Helper()
	old := FailoverAccount
	FailoverAccount = fake
	t.Cleanup(func() { FailoverAccount = old })
}

// A mock turn that reports a usage limit once, then succeeds on the next account.
func TestUsageLimitMovesTheTurnToAnotherAccountOnce(t *testing.T) {
	calls, switches := 0, 0
	failoverWith(t, func(context.Context, profile.Profile, string, string, error) (*AccountSwitch, error) {
		switches++
		return &AccountSwitch{From: "work", To: "personal", Until: time.Now().Add(3 * time.Hour)}, nil
	})
	res, err := withAccountFailover(context.Background(), profile.Profile{Harness: profile.HarnessMock}, t.TempDir(), ExecutionOptions{}, func(ExecutionOptions, string) (Result, error) {
		calls++
		if calls == 1 {
			return Result{}, quotaFailure()
		}
		out, err := runMock(context.Background(), "RESPOND: done")
		return Result{Output: out}, err
	})
	if err != nil || res.Output != "done" || calls != 2 || switches != 1 {
		t.Fatalf("res=%q err=%v calls=%d switches=%d", res.Output, err, calls, switches)
	}
	if res.AccountSwitch == nil || res.AccountSwitch.From != "work" || res.AccountSwitch.To != "personal" {
		t.Fatalf("the switch was not recorded: %+v", res.AccountSwitch)
	}
}

func TestUsageLimitRetriesOnlyOnce(t *testing.T) {
	calls := 0
	failoverWith(t, func(context.Context, profile.Profile, string, string, error) (*AccountSwitch, error) {
		return &AccountSwitch{From: "a", To: "b"}, nil
	})
	_, err := withAccountFailover(context.Background(), profile.Profile{}, "", ExecutionOptions{}, func(ExecutionOptions, string) (Result, error) {
		calls++
		return Result{}, quotaFailure()
	})
	if err == nil || calls != 2 {
		t.Fatalf("err=%v calls=%d, want one retry", err, calls)
	}
}

func TestNoAllowedAccountKeepsTheLimitAndSaysWhy(t *testing.T) {
	failoverWith(t, func(context.Context, profile.Profile, string, string, error) (*AccountSwitch, error) {
		return nil, errors.New("work resets Tue 12:00")
	})
	calls := 0
	_, err := withAccountFailover(context.Background(), profile.Profile{}, "", ExecutionOptions{}, func(ExecutionOptions, string) (Result, error) {
		calls++
		return Result{}, quotaFailure()
	})
	if err == nil || calls != 1 || !strings.Contains(err.Error(), "session limit") || !strings.Contains(err.Error(), "resets Tue 12:00") {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestOtherFailuresDoNotSwitchAccounts(t *testing.T) {
	failoverWith(t, func(context.Context, profile.Profile, string, string, error) (*AccountSwitch, error) {
		t.Fatal("switched on a failure that is not a usage limit")
		return nil, nil
	})
	for _, failure := range []error{
		errors.New("parse error"),
		errors.New("quota mentioned by the model in its own answer"),
		providerfailure.Tag(providerfailure.Auth, errors.New("the account is not signed in")),
		providerfailure.Tag(providerfailure.Capacity, errors.New("service unavailable, at capacity")),
		providerfailure.Tag(providerfailure.Unavailable, errors.New("bad gateway")),
	} {
		if _, err := withAccountFailover(context.Background(), profile.Profile{}, "", ExecutionOptions{}, func(ExecutionOptions, string) (Result, error) { return Result{}, failure }); err == nil {
			t.Fatal("error lost")
		}
	}
}

// A real mock turn through runJournaled: the limit ends the first run, the real
// wrapper switches (fake account store) and the rerun succeeds.
func TestMockUsageLimitFailsOverThroughRunJournaled(t *testing.T) {
	switches := 0
	failoverWith(t, func(_ context.Context, _ profile.Profile, _, _ string, failure error) (*AccountSwitch, error) {
		switches++
		if !strings.Contains(failure.Error(), "resets in 3h") {
			t.Fatalf("failure = %v", failure)
		}
		return &AccountSwitch{From: "work", To: "personal"}, nil
	})
	res, err := runJournaled(context.Background(), profile.Profile{Name: "mock", Harness: profile.HarnessMock}, "SLEEP:0\nUSAGE_LIMIT: resets in 3h\nRESPOND: done", t.TempDir(), false, 0, JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{})
	if err != nil || res.Output != "done" || switches != 1 || res.AccountSwitch == nil || res.AccountSwitch.To != "personal" {
		t.Fatalf("res=%+v err=%v switches=%d", res, err, switches)
	}
}

func TestMockUsageLimitWithNoOtherAccountFails(t *testing.T) {
	failoverWith(t, func(context.Context, profile.Profile, string, string, error) (*AccountSwitch, error) {
		return nil, errors.New("work resets Tue 12:00")
	})
	_, err := runJournaled(context.Background(), profile.Profile{Name: "mock", Harness: profile.HarnessMock}, "SLEEP:0\nUSAGE_LIMIT: resets in 3h", t.TempDir(), false, 0, JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{})
	if err == nil || !strings.Contains(err.Error(), "session limit") || !strings.Contains(err.Error(), "resets Tue 12:00") {
		t.Fatalf("err = %v", err)
	}
}

// The first attempt did tool work in a session before the limit. The rerun must
// resume that exact session with a continue prompt, not start over.
func TestFailoverResumesTheExactSessionAfterWork(t *testing.T) {
	failoverWith(t, func(context.Context, profile.Profile, string, string, error) (*AccountSwitch, error) {
		return &AccountSwitch{From: "a", To: "b"}, nil
	})
	var resumed []string
	_, err := withAccountFailover(context.Background(), profile.Profile{}, "", ExecutionOptions{}, func(_ ExecutionOptions, resume string) (Result, error) {
		resumed = append(resumed, resume)
		if len(resumed) == 1 {
			return Result{SessionID: "sess-1", Output: "ran a tool"}, quotaFailure()
		}
		return Result{SessionID: resume}, nil
	})
	if err != nil || len(resumed) != 2 || resumed[0] != "" || resumed[1] != "sess-1" {
		t.Fatalf("err=%v resume ids=%q", err, resumed)
	}
}

func TestFailoverDoesNotReplayWorkThatHasNoSessionToResume(t *testing.T) {
	failoverWith(t, func(context.Context, profile.Profile, string, string, error) (*AccountSwitch, error) {
		t.Fatal("moved a turn that cannot resume")
		return nil, nil
	})
	calls := 0
	_, err := withAccountFailover(context.Background(), profile.Profile{}, "", ExecutionOptions{}, func(ExecutionOptions, string) (Result, error) {
		calls++
		return Result{Output: "did work"}, quotaFailure()
	})
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "not replayed") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// The failed account is the turn's own, as the runtime pin reported it.
func TestFailoverBlamesTheTurnsAccount(t *testing.T) {
	var blamed string
	failoverWith(t, func(_ context.Context, _ profile.Profile, _ string, accountID string, _ error) (*AccountSwitch, error) {
		blamed = accountID
		return &AccountSwitch{From: "a", To: "b"}, nil
	})
	calls := 0
	_, _ = withAccountFailover(context.Background(), profile.Profile{}, "", ExecutionOptions{}, func(ExecutionOptions, string) (Result, error) {
		calls++
		if calls == 1 {
			return Result{AccountID: "account-a"}, quotaFailure()
		}
		return Result{}, nil
	})
	if blamed != "account-a" {
		t.Fatalf("blamed %q", blamed)
	}
}

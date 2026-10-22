package subagent

import (
	"context"
	"strings"
	"testing"
)

func TestDormantServiceAuthority(t *testing.T) {
	for _, process := range []string{"subagent", "workflow"} {
		t.Run(process, func(t *testing.T) {
			t.Setenv("DYNA_MODE", process)
			t.Setenv("DYNA_SESSION", "ses-test")
			m := &Manager{}
			checks := []struct {
				action string
				call   func() error
			}{
				{"room clear-dormant", func() error { _, err := m.ClearDormantSessions(context.Background(), "missing"); return err }},
				{"DismissSession", func() error { _, err := m.DismissSession(context.Background(), "missing"); return err }},
				{"subagent dismiss-pending", func() error { return m.DismissPendingPost(context.Background(), "missing", "missing", false) }},
			}
			for _, check := range checks {
				if err := check.call(); err == nil || !strings.HasPrefix(err.Error(), check.action+" is ") {
					t.Errorf("%s: %v", check.action, err)
				}
			}
		})
	}
}

func TestPendingDismissRootAuthority(t *testing.T) {
	t.Setenv("DYNA_MODE", "root")
	t.Setenv("DYNA_SESSION", "ses-root")
	err := (&Manager{}).DismissPendingPost(context.Background(), "missing", "missing", false)
	if err == nil || !strings.Contains(err.Error(), "explicit confirmation") {
		t.Fatalf("root did not reach confirmation check: %v", err)
	}
}

func TestDormantServicesRejectBareStamps(t *testing.T) {
	for _, key := range []string{"DYNA_SESSION", "DYNA_WORKER_RUN", "DYNA_WORKER_AGENT"} {
		t.Run(key, func(t *testing.T) {
			ownerDismissEnv(t)
			t.Setenv(key, "bare-stamp")
			m := &Manager{}
			_, err := m.ClearDormantSessions(context.Background(), "missing")
			if err == nil || !strings.HasPrefix(err.Error(), "room clear-dormant is ") {
				t.Fatalf("clear: %v", err)
			}
			_, err = m.DismissSession(context.Background(), "missing")
			if err == nil || !strings.HasPrefix(err.Error(), "DismissSession is ") {
				t.Fatalf("dismiss: %v", err)
			}
			err = m.DismissPendingPost(context.Background(), "missing", "missing", false)
			if err == nil || !strings.HasPrefix(err.Error(), "subagent dismiss-pending is ") {
				t.Fatalf("pending: %v", err)
			}
		})
	}
}

package subagent

import (
	"testing"
	"time"

	"dyna-agent/internal/notices"
)

func TestCooldownNoticeShowsOnlyTheProvidersResetTime(t *testing.T) {
	s := notices.Store{Dir: t.TempDir()}
	at := time.Date(2026, 10, 26, 20, 34, 0, 0, time.UTC)
	if err := postCooldownNotice(s, "terra", "You've hit your usage limit.", at); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || !list[0].Provider.ResetsAt.IsZero() {
		t.Fatalf("list = %+v, %v; want no reset time when the provider named none", list, err)
	}
	if err := postCooldownNotice(s, "terra", "usage limit, resets in 100h", at); err != nil {
		t.Fatal(err)
	}
	if list, err = s.List(); err != nil || !list[0].Provider.ResetsAt.Equal(at.Add(100*time.Hour)) {
		t.Fatalf("list = %+v, %v; want the stated reset", list, err)
	}
}

package store

import (
	"testing"
	"time"
)

func TestRenewalsStayBoundedPerAccount(t *testing.T) {
	s := registryStore(t)
	first := saveAccount(t, s, "first")
	var second Metadata
	if err := s.Update(func(st *State) error {
		var err error
		second, err = st.Save("fixture", "", "second", "Second", grant("second"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 28, 21, 0, 0, 0, time.UTC)
	record := func(account Metadata, at time.Time) {
		t.Helper()
		if err := s.RecordRenewal(t.Context(), Renewal{
			AccountID: account.ID, Harness: "pi", Provider: ProviderChatGPT,
			AttemptedAt: at, Result: RenewalResultInvalidGrant, Reason: "invalid_grant",
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i <= 20; i++ {
		record(first, base.Add(time.Duration(i)*time.Minute))
	}
	record(second, base.Add(time.Hour))

	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	var oldest int64
	if err := db.QueryRow("SELECT COUNT(*),MIN(attempted_at) FROM account_renewals WHERE account_id=?", first.ID).Scan(&count, &oldest); err != nil {
		t.Fatal(err)
	}
	if count != 20 || oldest != base.Add(time.Minute).UnixNano() {
		t.Fatalf("first account kept %d rows from %v, want 20 rows from %v", count, time.Unix(0, oldest), base.Add(time.Minute))
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM account_renewals WHERE account_id=?", second.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("second account kept %d rows, %v; want 1", count, err)
	}
	latest, err := s.LatestRenewals(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 || !latest[first.ID].AttemptedAt.Equal(base.Add(20*time.Minute)) || !latest[second.ID].AttemptedAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("latest renewals = %+v", latest)
	}
}

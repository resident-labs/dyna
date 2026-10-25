package service

import (
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

func TestUsageReaderResultWithASecretIsRejected(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	const id = "0123456789abcdef0123456789abcdef"
	st := &store.Store{Root: t.TempDir()}
	if err := st.Update(func(state *store.State) error {
		state.Entries = append(state.Entries, store.Entry{Metadata: store.Metadata{
			ID: id, Harness: "pi", Provider: store.ProviderDeepSeek, Kind: store.KindAPIKey,
			Identity: "pi:deepseek", Label: "test",
		}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: st, Now: func() time.Time { return now }})
	captured := native.Captured{Identity: "pi:deepseek", Credential: store.Credential{
		Schema:  "pi/native-files@1",
		Payload: []byte(`{"auth.json":{"deepseek":{"type":"api_key","key":"fixture-secret-value"}}}`),
	}}
	for _, tc := range []struct {
		name        string
		observation store.Observation
	}{
		{"copied credential", store.Observation{ObservedAt: now, Windows: []store.Window{{ID: "balance", Label: "Balance fixture-secret-value"}}}},
		{"token-like value", store.Observation{ObservedAt: now, Windows: []store.Window{{ID: "balance", Label: "sk-live-0123456789abcdef"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := Account{ID: id, Harness: "pi", Provider: store.ProviderDeepSeek, Status: "ready"}
			if err := s.saveNativeUsageObservation(&account, captured, tc.observation, nil); err != nil {
				t.Fatal(err)
			}
			if account.UsageReason != store.UsageReasonSecret || account.UsageError != readerSecretDetail || len(account.Windows) != 0 {
				t.Fatalf("account kept a leaked result: %+v", account)
			}
			state, err := st.Read()
			if err != nil {
				t.Fatal(err)
			}
			saved := state.Find(id)
			if saved == nil || saved.Observation == nil || saved.Observation.Reason != store.UsageReasonSecret || saved.Observation.UsageError != readerSecretDetail || len(saved.Observation.Windows) != 0 {
				t.Fatalf("saved observation kept a leaked result: %+v", saved)
			}
			if err := st.Update(func(state *store.State) error {
				state.Find(id).Observation = nil
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

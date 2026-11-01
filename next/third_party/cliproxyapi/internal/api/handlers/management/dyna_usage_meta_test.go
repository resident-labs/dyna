package management

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func metaSignals(after time.Duration) map[string]string {
	reset := strconv.FormatInt(time.Now().Add(after).Unix(), 10)
	return map[string]string{
		helps.MetaQuotaWindowUsedHeader:    "12",
		helps.MetaQuotaWindowMinutesHeader: "300",
		helps.MetaQuotaWindowResetHeader:   reset,
		helps.MetaQuotaWeeklyUsedHeader:    "104.5",
		helps.MetaQuotaWeeklyResetHeader:   reset,
	}
}

func TestDynaUsageMetaReadsObservedSignalsWithoutARequest(t *testing.T) {
	fake := dynaFixedUsage(t, http.StatusInternalServerError, ``)
	observed := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	auth := &coreauth.Auth{ID: "invented", Provider: "meta", Metadata: map[string]any{"api_key": "invented-key"}, Quota: coreauth.QuotaState{ObservedAt: observed, Signals: metaSignals(time.Hour)}}
	h := dynaUsageHandler(t, auth)
	entry := dynaOneEntry(t, h)
	if entry.State != dynaUsageOK || len(entry.Windows) != 2 || entry.ObservedAt != observed.Format(time.RFC3339) {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.Windows[0].Kind != "five_hour" || entry.Windows[0].Utilization != 12 || entry.Windows[1].Kind != "weekly" || entry.Windows[1].Utilization != 104.5 {
		t.Fatalf("windows = %#v", entry.Windows)
	}
	if len(fake.requests) != 0 {
		t.Fatal("the Meta reader made a request")
	}
}

func TestDynaUsageMetaUnknownUntilObserved(t *testing.T) {
	auth := &coreauth.Auth{ID: "invented", Provider: "meta", Attributes: map[string]string{"api_key": "invented-key"}}
	if entry := dynaOneEntry(t, dynaUsageHandler(t, auth)); entry.State != dynaUsageUnknown || len(entry.Windows) != 0 {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestDynaUsageMetaStaleWindowIsUnknown(t *testing.T) {
	if usage := observedMetaUsage(&coreauth.Auth{Quota: coreauth.QuotaState{Signals: metaSignals(-time.Hour)}}, time.Now()); len(usage.windows) != 0 {
		t.Fatalf("windows = %#v", usage.windows)
	}
}

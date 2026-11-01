package management

import (
	"net/http"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestDynaUsageDeepSeekKeepsCurrenciesApart(t *testing.T) {
	usage := dynaParse(t, parseDeepSeekUsage, `{"is_available":true,"balance_infos":[
		{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00","topped_up_balance":"100.00"},
		{"currency":"USD","total_balance":"3.50","granted_balance":"0.00","topped_up_balance":"3.50"}]}`)
	if len(usage.balances) != 2 || usage.balances[0] != (dynaQuotaBalance{Currency: "CNY", Amount: "110.00"}) || usage.balances[1] != (dynaQuotaBalance{Currency: "USD", Amount: "3.50"}) {
		t.Fatalf("balances = %#v", usage.balances)
	}
}

func TestDynaUsageDeepSeekLowBalanceIsStillABalance(t *testing.T) {
	fake := dynaFixedUsage(t, http.StatusOK, `{"is_available":false,"balance_infos":[{"currency":"USD","total_balance":"0.00"}]}`)
	auth := &coreauth.Auth{ID: "invented", Provider: "openai-compatible-deepseek", Attributes: map[string]string{"api_key": "invented-key"}}
	entry := dynaOneEntry(t, dynaUsageHandler(t, auth))
	if entry.State != dynaUsageOK || entry.Error != "" || len(entry.Balances) != 1 || entry.Balances[0].Amount != "0.00" {
		t.Fatalf("entry = %#v", entry)
	}
	request := fake.requests[0]
	if request.URL.String() != "https://api.deepseek.com/user/balance" || request.Header.Get("Authorization") != "Bearer invented-key" {
		t.Fatalf("request = %s %#v", request.URL, request.Header)
	}
}

// The live response for an overdrawn account.
func TestDynaUsageDeepSeekShowsANegativeBalance(t *testing.T) {
	dynaFixedUsage(t, http.StatusOK, `{"is_available": false, "balance_infos": [{"currency": "USD", "total_balance": "-0.01", "granted_balance": "0.00", "topped_up_balance": "-0.01"}]}`)
	auth := &coreauth.Auth{ID: "invented", Provider: "openai-compatible-deepseek", Attributes: map[string]string{"api_key": "invented-key"}}
	entry := dynaOneEntry(t, dynaUsageHandler(t, auth))
	if entry.State != dynaUsageOK || len(entry.Balances) != 1 || entry.Balances[0] != (dynaQuotaBalance{Currency: "USD", Amount: "-0.01"}) {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestDynaUsageDeepSeekUnknownShapeIsNotZero(t *testing.T) {
	for _, body := range []string{`{}`, `{"balance_infos":[]}`, `{"balance_infos":[{"currency":"USD"}]}`, `{"balance_infos":[{"total_balance":"1.00"}]}`, `{"balance_infos":[{"currency":"USD","total_balance":"n/a"}]}`} {
		if usage := dynaParse(t, parseDeepSeekUsage, body); len(usage.balances) != 0 {
			t.Fatalf("%s produced %#v", body, usage.balances)
		}
	}
}

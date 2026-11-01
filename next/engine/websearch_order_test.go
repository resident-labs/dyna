package engine

import (
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"testing"
)

func TestSearchAccountsUsesEligibleHeadroom(t *testing.T) {
	a, b, c := 10.0, 90.0, 100.0
	credentials := []model.Credential{
		{ID: "best", Provider: "codex", State: model.CredentialConnected, Quota: model.CredentialQuota{Primary: model.CredentialQuotaWindow{UsedPercent: &a}}},
		{ID: "limited", Provider: "codex", State: model.CredentialConnected, Quota: model.CredentialQuota{Primary: model.CredentialQuotaWindow{UsedPercent: &b}, Secondary: model.CredentialQuotaWindow{UsedPercent: &c}}},
		{ID: "expired", Provider: "codex", State: model.CredentialExpired},
	}
	ids, headroom, reason := searchAccounts(credentials, []string{"best", "limited", "expired"}, "codex")
	if len(ids) != 1 || ids[0] != "best" || headroom != 90 || reason != "" {
		t.Fatalf("%v %v %q", ids, headroom, reason)
	}
	_, _, reason = searchAccounts(credentials, []string{"limited"}, "codex")
	if reason != contract.WebSearchNoQuota {
		t.Fatalf("reason %q", reason)
	}
	_, _, reason = searchAccounts(credentials, []string{"expired"}, "codex")
	if reason != contract.WebSearchNotSignedIn {
		t.Fatalf("reason %q", reason)
	}
}

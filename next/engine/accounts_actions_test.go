package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/store"
)

func TestAccountActionKeepsSecretOutOfAudit(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	core := New(st, nil, nil, time.Now)
	actions := NewAccountActions(core, nil)
	if err := core.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": actions.Entry()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	s := actions
	secret := "invented-private-api-key"
	result, problem := s.runAccountAction(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.AccountActionSetKey, func(context.Context) accountActionResult {
		if secret == "" {
			t.Fatal("missing invented secret")
		}
		return accountActionResult{}
	})
	if problem != nil || result.err != nil {
		t.Fatal("account action failed")
	}
	events, err := st.OwnerEvents(ctx, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatal("missing audit event")
	}
	eventJSON, err := json.Marshal(events)
	if err != nil || strings.Contains(string(eventJSON), secret) {
		t.Fatal("audit contained secret")
	}
	var args, card, opResult string
	if err := st.Read().QueryRow("SELECT args,card,result FROM operations WHERE room IS NULL").Scan(&args, &card, &opResult); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(args+card+opResult, secret) {
		t.Fatal("operation contained secret")
	}
}

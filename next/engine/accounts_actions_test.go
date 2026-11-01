package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

func TestReconnectUnavailableBeforeLogin(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: "anonymous", Provider: "kimi", Record: []byte(`{"metadata":{"type":"kimi"}}`)}, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "account", "anonymous", contract.ProviderXAI, "label"); err != nil {
		t.Fatal(err)
	}
	proxy, err := model.NewProxy(t.TempDir(), "key")
	if err != nil {
		t.Fatal(err)
	}
	core := New(st, nil, nil, time.Now)
	actions := NewAccountActions(core, proxy.Connections("key", st, time.Now), proxy.Quotas("key", st, time.Now))
	if err := core.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": actions.Entry()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	_, problem := actions.AccountsReconnect(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.AccountsReconnectRequest{AccountID: "account"})
	if problem == nil || problem.Code != contract.ErrReconnectUnavailable {
		t.Fatalf("got error %v, want reconnect_unavailable", problem)
	}
}

func TestAccountActionKeepsSecretOutOfAudit(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	proxy, err := model.NewProxy(t.TempDir(), "invented-management-key")
	if err != nil {
		t.Fatal(err)
	}
	secret := "invented-private-api-key"
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dyna/credentials/activate" {
			_, _ = w.Write([]byte("{}"))
			return
		}
		var input struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Key      string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Key != secret {
			t.Error("fake proxy did not receive the key")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: input.ID, Provider: input.Provider, Record: []byte("{}")}, true); err != nil {
			t.Error("fake credential save failed")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("{}"))
	})}
	go func() { _ = server.Serve(proxy.Listener) }()
	defer server.Close()
	core := New(st, nil, nil, time.Now)
	actions := NewAccountActions(core, proxy.Connections("invented-management-key", st, time.Now), proxy.Quotas("invented-management-key", st, time.Now))
	if err := core.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": actions.Entry()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	_, problem := actions.AccountsSetKey(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.AccountsSetKeyRequest{Provider: contract.ProviderZAI, Label: "Invented", Key: secret})
	if problem != nil {
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

func TestAccountLimitsOwnerAction(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: "proxy", Provider: "claude", Record: []byte(`{}`)}, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "account", "proxy", contract.ProviderAnthropic, "label"); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','room',0,1,1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	core := New(st, nil, nil, time.Now)
	actions := NewAccountActions(core, nil, nil)
	if err := core.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": actions.Entry()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	req := contract.AccountsSetLimitsRequest{AccountID: "account", Rooms: []contract.RoomID{"r"}}
	if _, problem := actions.AccountsSetLimits(ctx, contract.Principal{Kind: contract.PrincipalSession}, req); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session: %v", problem)
	}
	if _, problem := actions.AccountsSetLimits(ctx, contract.Principal{Kind: contract.PrincipalOwner}, req); problem != nil {
		t.Fatal(problem)
	}
	ids, err := st.EligibleAccountProxyIDs(ctx, "other")
	if err != nil || len(ids) != 0 {
		t.Fatalf("policy %v %v", ids, err)
	}
	req.Rooms = []contract.RoomID{"missing"}
	if _, problem := actions.AccountsSetLimits(ctx, contract.Principal{Kind: contract.PrincipalOwner}, req); problem == nil {
		t.Fatal("unknown room accepted")
	}
	ids, err = st.EligibleAccountProxyIDs(ctx, "r")
	if err != nil || len(ids) != 1 {
		t.Fatalf("rollback %v %v", ids, err)
	}
}

func TestAccountRename(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, id := range []string{"one", "two"} {
		if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: id, Provider: "claude", Record: []byte("{}")}, true); err != nil {
			t.Fatal(err)
		}
		if err := st.BindAccount(ctx, contract.AccountID(id), id, contract.ProviderAnthropic, id); err != nil {
			t.Fatal(err)
		}
	}
	core := New(st, nil, nil, time.Now)
	actions := NewAccountActions(core, nil, nil)
	if err := core.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": actions.Entry()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	label := func(id contract.AccountID) string {
		t.Helper()
		bindings, err := st.Accounts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, binding := range bindings {
			if binding.Account.ID == id {
				return binding.Account.Label
			}
		}
		t.Fatalf("account %s is gone", id)
		return ""
	}

	if _, problem := actions.AccountsRename(ctx, contract.Principal{Kind: contract.PrincipalSession}, contract.AccountsRenameRequest{AccountID: "one", Label: "Work"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session: %v", problem)
	}
	if got := label("one"); got != "one" {
		t.Fatalf("session renamed the account to %q", got)
	}

	for name, value := range map[string]string{"empty": "", "blank": " \t ", "too long": strings.Repeat("a", 129), "line feed": "a\nb", "return": "a\rb", "at sign": "me@example.test"} {
		if _, problem := actions.AccountsRename(ctx, owner, contract.AccountsRenameRequest{AccountID: "one", Label: value}); problem == nil || problem.Code != contract.ErrInvalid {
			t.Fatalf("%s: %v", name, problem)
		}
	}
	if got := label("one"); got != "one" {
		t.Fatalf("an invalid label was stored: %q", got)
	}

	if _, problem := actions.AccountsRename(ctx, owner, contract.AccountsRenameRequest{AccountID: "missing", Label: "Work"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("unknown account: %v", problem)
	}

	for _, tc := range []struct {
		id          contract.AccountID
		input, want string
	}{
		{"one", "  Work  ", "Work"},
		{"one", "Work", "Work"}, // the same label again is not an unknown account
		{"two", "Work", "Work"}, // labels are display names, and connecting already allows a repeat
		{"one", strings.Repeat("a", 128), strings.Repeat("a", 128)},
	} {
		if _, problem := actions.AccountsRename(ctx, owner, contract.AccountsRenameRequest{AccountID: tc.id, Label: tc.input}); problem != nil {
			t.Fatalf("rename %s to %q: %v", tc.id, tc.input, problem)
		}
		if got := label(tc.id); got != tc.want {
			t.Fatalf("%s is %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestOAuthImportRejectsSession(t *testing.T) {
	actions := &AccountActions{}
	_, problem := actions.AccountsImportOAuth(context.Background(), contract.Principal{Kind: contract.PrincipalSession}, contract.AccountsImportOAuthRequest{Provider: contract.ProviderKimi, Label: "Example", ExpiresAt: time.Unix(2000000000, 0), Credential: `{"access_token":"invented","refresh_token":"invented"}`})
	if problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatal("session was not denied")
	}
}

func TestOAuthImportProviders(t *testing.T) {
	for _, provider := range []contract.AccountProvider{contract.ProviderAnthropic, contract.ProviderOpenAI, contract.ProviderKimi, contract.ProviderXAI} {
		if _, ok := model.OAuthProxyProvider(provider); !ok {
			t.Fatalf("%s has no proxy provider", provider)
		}
	}
	actions := &AccountActions{}
	_, problem := actions.AccountsImportOAuth(context.Background(), contract.Principal{Kind: contract.PrincipalOwner}, contract.AccountsImportOAuthRequest{Provider: contract.ProviderZAI, Label: "Example", ExpiresAt: time.Unix(2000000000, 0), Credential: `{"access_token":"invented","refresh_token":"invented"}`})
	if problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatal("key provider was not rejected")
	}
}

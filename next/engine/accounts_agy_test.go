package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/agylogin"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

const agyTokenSentinel = "SENTINEL-AGY-TOKEN-77c1"

// fakeAgyLogin prints a valid sign-in link, reads one line and writes a token file. It is never the real program.
const fakeAgyLogin = `#!/bin/sh
while [ $# -gt 0 ]; do [ "$1" = --gemini-dir ] && dir=$2; shift; done
echo "Open: $FAKE_LINK" >&2
read line || exit 3
mkdir -p "$dir/antigravity-acp"
printf %s "` + agyTokenSentinel + `" > "$dir/antigravity-acp/antigravity-oauth-token"
`

func TestAgyAccountIsAddedListedRenamedAndRemoved(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	layout := box.Layout{Data: dir, HostData: filepath.Join(dir, "host")}
	if err := os.MkdirAll(filepath.Dir(layout.HostAgyBinary()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.HostAgyBinary(), []byte(fakeAgyLogin), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_LINK", "https://accounts.google.com/o/oauth2/auth?response_type=code&state=S&redirect_uri="+url.QueryEscape("https://antigravity.google/oauth-callback"))
	proxy, err := model.NewProxy(t.TempDir(), t.TempDir(), "k")
	if err != nil {
		t.Fatal(err)
	}
	// Nothing serves the proxy: its listing fails at once, as AccountsList tolerates.
	proxy.Listener.Close()
	core := New(st, nil, nil, time.Now)
	core.SetLayout(layout)
	actions := NewAccountActions(core, proxy.Connections("k", st, time.Now), proxy.Quotas("k", st, time.Now))
	logins := agylogin.New(agylogin.Config{Layout: layout, NewID: store.NewAgyAccountID, Add: func(ctx context.Context, id contract.AccountID, label string) error {
		return st.AddAgyAccount(ctx, id, label, time.Now())
	}})
	actions.SetAgyLogin(logins)
	if err := core.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": actions.Entry()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, problem := actions.AccountsConnect(ctx, contract.Principal{Kind: contract.PrincipalSession}, contract.AccountsConnectRequest{Provider: contract.ProviderAgy, Label: "x"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session: %v", problem)
	}
	started, problem := actions.AccountsConnect(ctx, owner, contract.AccountsConnectRequest{Provider: contract.ProviderAgy, Label: " Second "})
	if problem != nil || started.Flow != contract.ConnectFlowCode || started.Link != os.Getenv("FAKE_LINK") {
		t.Fatalf("connect: %+v %v", started, problem)
	}
	if _, problem := actions.AccountsConnect(ctx, owner, contract.AccountsConnectRequest{Provider: contract.ProviderAgy, Label: "again"}); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("a second sign-in: %v", problem)
	}
	if _, problem := actions.AccountsConnectCallback(ctx, owner, contract.AccountsConnectCallbackRequest{Attempt: started.Attempt, RedirectURL: "two words"}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("bad paste: %v", problem)
	}
	if _, problem := actions.AccountsConnectCallback(ctx, owner, contract.AccountsConnectCallbackRequest{Attempt: started.Attempt, RedirectURL: "the-code"}); problem != nil {
		t.Fatal(problem)
	}
	logins.Wait(started.Attempt)
	status, problem := actions.AccountsConnectStatus(ctx, owner, contract.AccountsConnectStatusRequest{Attempt: started.Attempt})
	if problem != nil || status.Status != contract.ConnectConnected || status.AccountID == "" {
		t.Fatalf("status: %+v %v", status, problem)
	}
	id := status.AccountID
	listed, problem := actions.AccountsList(ctx, owner, contract.AccountsListRequest{})
	if problem != nil || len(listed.Accounts) != 1 || listed.Accounts[0].ID != id || listed.Accounts[0].Provider != contract.ProviderAgy || listed.Accounts[0].Label != "Second" || listed.Accounts[0].State != contract.AccountConnected {
		t.Fatalf("list: %+v %v", listed, problem)
	}
	var found bool
	for _, p := range listed.Providers {
		found = found || p.ID == contract.ProviderAgy && p.ConnectFlow == contract.ConnectFlowCode
	}
	if !found {
		t.Fatalf("providers: %+v", listed.Providers)
	}
	if _, problem := actions.AccountsRename(ctx, owner, contract.AccountsRenameRequest{AccountID: id, Label: "Renamed"}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := actions.AccountsRename(ctx, owner, contract.AccountsRenameRequest{AccountID: "agy_missing", Label: "x"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("rename missing: %v", problem)
	}
	if _, problem := actions.AccountsSetLimits(ctx, owner, contract.AccountsSetLimitsRequest{AccountID: id}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("limits: %v", problem)
	}
	// The token is in its file and nowhere else Dyna writes: not in an event, not in the store's files.
	events, err := st.OwnerEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(events)
	if bytes.Contains(raw, []byte(agyTokenSentinel)) {
		t.Fatal("an event holds the token")
	}
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && path != layout.HostAgyToken(string(id)) && path != layout.HostAgyBinary() {
			if b, _ := os.ReadFile(path); bytes.Contains(b, []byte(agyTokenSentinel)) {
				t.Errorf("%s holds the token", path)
			}
		}
		return nil
	})
	if _, err := os.Stat(layout.HostAgyToken(string(id))); err != nil {
		t.Fatal(err)
	}
	if _, problem := actions.AccountsRemove(ctx, owner, contract.AccountsRemoveRequest{AccountID: id}); problem != nil {
		t.Fatal(problem)
	}
	if _, err := os.Stat(layout.HostAgyToken(string(id))); !os.IsNotExist(err) {
		t.Fatalf("token file after remove: %v", err)
	}
	if left, _ := st.AgyAccounts(ctx); len(left) != 0 {
		t.Fatalf("rows after remove: %v", left)
	}
	if _, problem := actions.AccountsRemove(ctx, owner, contract.AccountsRemoveRequest{AccountID: id}); problem == nil {
		t.Fatal("removing a removed account succeeded")
	}
}

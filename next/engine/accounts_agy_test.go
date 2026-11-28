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
echo "  $FAKE_LINK"
[ -n "$FAKE_ERR" ] && echo "$FAKE_ERR" >&2
read line || exit 3
[ "${FAKE_EXIT:-0}" != 0 ] && exit $FAKE_EXIT
mkdir -p "$dir/antigravity-acp"
printf %s "` + agyTokenSentinel + `" > "$dir/antigravity-acp/antigravity-oauth-token"
`

type agyRig struct {
	ctx     context.Context
	st      *store.Store
	layout  box.Layout
	actions *AccountActions
	logins  *agylogin.Logins
}

func newAgyRig(t *testing.T) *agyRig {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
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
	}, Remove: st.RemoveAgyAccount})
	actions.SetAgyLogin(logins)
	if err := core.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"accounts.action": actions.Entry()}, time.Minute); err != nil {
		t.Fatal(err)
	}
	return &agyRig{ctx: ctx, st: st, layout: layout, actions: actions, logins: logins}
}

func TestAgyAccountIsAddedListedRenamedAndRemoved(t *testing.T) {
	r := newAgyRig(t)
	ctx, st, layout, actions, logins := r.ctx, r.st, r.layout, r.actions, r.logins
	dir := filepath.Dir(layout.HostData)
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

// Cancel through the action kills the engine side: the owner can add again at once, and nothing is left.
func TestAgyCancelThroughTheActionFreesTheNextAdd(t *testing.T) {
	r := newAgyRig(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	first, problem := r.actions.AccountsConnect(r.ctx, owner, contract.AccountsConnectRequest{Provider: contract.ProviderAgy, Label: "a"})
	if problem != nil {
		t.Fatal(problem)
	}
	if _, problem := r.actions.AccountsConnect(r.ctx, owner, contract.AccountsConnectRequest{Provider: contract.ProviderAgy, Label: "b"}); problem == nil || problem.Code != contract.ErrConflict || problem.Message != "an Antigravity sign-in is already in progress" {
		t.Fatalf("while one runs: %v", problem)
	}
	if _, problem := r.actions.AccountsConnectCancel(r.ctx, contract.Principal{Kind: contract.PrincipalSession}, contract.AccountsConnectCancelRequest{Attempt: first.Attempt}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session cancel: %v", problem)
	}
	if _, problem := r.actions.AccountsConnectCancel(r.ctx, owner, contract.AccountsConnectCancelRequest{Attempt: "other"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("another attempt: %v", problem)
	}
	if _, problem := r.actions.AccountsConnectCancel(r.ctx, owner, contract.AccountsConnectCancelRequest{Attempt: first.Attempt}); problem != nil {
		t.Fatal(problem)
	}
	if left, _ := filepath.Glob(r.layout.HostAgyLoginPattern()); len(left) != 0 {
		t.Fatalf("folders after cancel: %v", left)
	}
	if _, problem := r.actions.AccountsConnect(r.ctx, owner, contract.AccountsConnectRequest{Provider: contract.ProviderAgy, Label: "b"}); problem != nil {
		t.Fatalf("add after cancel: %v", problem)
	}
}

// A sign-in whose output reads as a refusal ends as restricted, which the page words fixedly and offers no retry for; the text itself
// is in no status, event or file.
func TestAgyRefusalEndsRestricted(t *testing.T) {
	r := newAgyRig(t)
	t.Setenv("FAKE_EXIT", "1")
	t.Setenv("FAKE_ERR", "Error 403: PERMISSION-DENIED-TEXT-31f")
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	started, problem := r.actions.AccountsConnect(r.ctx, owner, contract.AccountsConnectRequest{Provider: contract.ProviderAgy, Label: "a"})
	if problem != nil {
		t.Fatal(problem)
	}
	if _, problem := r.actions.AccountsConnectCallback(r.ctx, owner, contract.AccountsConnectCallbackRequest{Attempt: started.Attempt, RedirectURL: "code"}); problem != nil {
		t.Fatal(problem)
	}
	r.logins.Wait(started.Attempt)
	status, problem := r.actions.AccountsConnectStatus(r.ctx, owner, contract.AccountsConnectStatusRequest{Attempt: started.Attempt})
	if problem != nil || status.Status != contract.ConnectFailed || status.Reason != contract.ConnectRestricted {
		t.Fatalf("status %+v %v", status, problem)
	}
	events, _ := r.st.OwnerEvents(r.ctx, 0, 100)
	raw, _ := json.Marshal(events)
	if bytes.Contains(raw, []byte("PERMISSION-DENIED-TEXT-31f")) {
		t.Fatal("the refusal text reached an event")
	}
}

// A row with no token file, which a crash between the insert and the rename leaves, is listed as needing a sign-in and can be removed.
func TestAgyRowWithoutATokenNeedsSignInAndCanBeRemoved(t *testing.T) {
	r := newAgyRig(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	id, _ := store.NewAgyAccountID()
	if err := r.st.AddAgyAccount(r.ctx, id, "crashed", time.Now()); err != nil {
		t.Fatal(err)
	}
	listed, problem := r.actions.AccountsList(r.ctx, owner, contract.AccountsListRequest{})
	if problem != nil || len(listed.Accounts) != 1 || listed.Accounts[0].State != contract.AccountSignInExpired || listed.Accounts[0].Reason != contract.AccountCredentialMissing {
		t.Fatalf("list: %+v %v", listed, problem)
	}
	if _, problem := r.actions.AccountsRemove(r.ctx, owner, contract.AccountsRemoveRequest{AccountID: id}); problem != nil {
		t.Fatal(problem)
	}
	if left, _ := r.st.AgyAccounts(r.ctx); len(left) != 0 {
		t.Fatalf("row left: %v", left)
	}
}

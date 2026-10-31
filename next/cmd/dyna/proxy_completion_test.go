package main

import (
	"context"
	"net"
	"net/rpc"
	"testing"

	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

type fakeCompletionService struct {
	store     *proxyStore
	activated bool
}

func (s *fakeCompletionService) DeleteDynaCredential(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}
func (s *fakeCompletionService) ActivateDynaCredential(context.Context, string, string, string) error {
	s.activated = true
	return nil
}

func TestRejectedOAuthCompletionRemovesOnlyStagedCredential(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, id := range []string{"late", "other"} {
		if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: id, Provider: "kimi", Record: []byte("{}")}, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.BindAccount(ctx, "account", "other", "kimi", "Invented"); err != nil {
		t.Fatal(err)
	}
	server, conn := net.Pipe()
	r := rpc.NewServer()
	if err := r.RegisterName("Auth", &model.AuthRPC{Store: st, Complete: func(context.Context, model.OAuthCompletion) error { return model.ErrCompletionRejected }}); err != nil {
		t.Fatal(err)
	}
	go r.ServeConn(server)
	client := rpc.NewClient(conn)
	defer client.Close()
	credentials := &proxyStore{client: client}
	service := &fakeCompletionService{store: credentials}
	if err := proxyOAuthComplete(client, credentials, service, "expired-state", "late"); err != nil {
		t.Fatal("ordinary expiry disrupted the proxy")
	}
	rows, err := st.ListProxyAuths(ctx)
	if err != nil || len(rows) != 1 || rows[0].ID != "other" || !rows[0].Bound || service.activated {
		t.Fatal("rejection affected another credential")
	}
}

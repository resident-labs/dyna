package model

import (
	"context"
	"errors"
	"io"
	"net/rpc"

	"github.com/resident-labs/dyna/engine/store"
)

// AuthRPC exchanges opaque proxy records over the inherited private connection.
type AuthRPC struct{ Store *store.Store }
type AuthDelete struct{ ID string }
type AuthSave struct {
	Record store.ProxyAuth
	Create bool
}

func (r *AuthRPC) List(_ struct{}, out *[]store.ProxyAuth) error {
	records, err := r.Store.ListProxyAuths(context.Background())
	if err != nil {
		return errors.New("proxy store unavailable")
	}
	*out = records
	return nil
}

func (r *AuthRPC) Save(req AuthSave, _ *struct{}) error {
	if err := r.Store.SaveProxyAuth(context.Background(), req.Record, req.Create); err != nil {
		return errors.New("proxy store unavailable")
	}
	return nil
}

func (r *AuthRPC) Import(records []store.ImportedProxyAuth, _ *struct{}) error {
	if err := r.Store.ImportProxyAuths(context.Background(), records); err != nil {
		return errors.New("proxy import failed")
	}
	return nil
}

func (r *AuthRPC) Delete(req AuthDelete, _ *struct{}) error {
	if err := r.Store.DeleteProxyAuth(context.Background(), req.ID); err != nil {
		return errors.New("proxy store unavailable")
	}
	return nil
}

func serveAuthRPC(conn io.ReadWriteCloser, st *store.Store) {
	server := rpc.NewServer()
	_ = server.RegisterName("Auth", &AuthRPC{Store: st})
	server.ServeConn(conn)
}

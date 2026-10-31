package model

import (
	"context"
	"errors"
	"io"
	"net/rpc"

	"github.com/resident-labs/dyna/contract/private"
)

// AuthRPC exchanges opaque proxy records over the inherited private connection.
type AuthRPC struct {
	Store    AuthStore
	Complete func(context.Context, OAuthCompletion) error
}

type OAuthCompletionResult struct{ Accepted bool }

type OAuthCompletion struct {
	State  string
	AuthID string
}
type AuthDelete struct{ ID string }
type AuthSave struct {
	Record private.ProxyAuth
	Create bool
}

func (r *AuthRPC) List(_ struct{}, out *[]private.ProxyAuth) error {
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

func (r *AuthRPC) Delete(req AuthDelete, _ *struct{}) error {
	if err := r.Store.DeleteProxyAuth(context.Background(), req.ID); err != nil {
		return errors.New("proxy store unavailable")
	}
	return nil
}

func (r *AuthRPC) OAuthCompleted(req OAuthCompletion, out *OAuthCompletionResult) error {
	if r.Complete == nil {
		return errors.New("account completion unavailable")
	}
	err := r.Complete(context.Background(), req)
	if errors.Is(err, ErrCompletionRejected) {
		return nil
	}
	if err != nil {
		return errors.New("account completion unavailable")
	}
	out.Accepted = true
	return nil
}

func serveAuthRPC(conn io.ReadWriteCloser, st AuthStore, complete func(context.Context, OAuthCompletion) error) {
	server := rpc.NewServer()
	_ = server.RegisterName("Auth", &AuthRPC{Store: st, Complete: complete})
	server.ServeConn(conn)
}

package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/rpc"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/private"
)

// AuthRPC exchanges opaque proxy records over the inherited private connection.
type AuthRPC struct {
	Store       AuthStore
	Complete    func(context.Context, OAuthCompletion) error
	Connections *Connections
}

type OAuthCompletionResult struct {
	Accepted bool
	Prefix   string
	Model    string
}

type OAuthCompletion struct {
	State  string
	AuthID string
}
type AuthDelete struct{ ID string }
type AuthSave struct {
	Record   private.ProxyAuth
	Create   bool
	Attempt  string
	Identity OAuthIdentity
}

// OAuthIdentity carries only raw provider account identifiers on the private RPC.
type OAuthIdentity struct {
	AccountID   string `json:"account_id"`
	AccountUUID string `json:"account_uuid"`
	Subject     string `json:"sub"`
	Email       string `json:"email"`
}

func (r *AuthRPC) List(_ struct{}, out *[]private.ProxyAuth) error {
	records, err := r.Store.ListProxyAuths(context.Background())
	if err != nil {
		return errors.New("proxy store unavailable")
	}
	*out = records
	return nil
}

// rawCredentialIdentity reads only provider identity fields from an opaque proxy record.
func rawCredentialIdentity(raw []byte) (string, string) {
	var record struct {
		Metadata OAuthIdentity `json:"metadata"`
	}
	if json.Unmarshal(raw, &record) != nil {
		return "", ""
	}
	return record.Metadata.identity()
}

func (identity OAuthIdentity) identity() (string, string) {
	for _, field := range []struct{ name, value string }{
		{"account_id", identity.AccountID},
		{"account_uuid", identity.AccountUUID},
		{"sub", identity.Subject},
		{"email", identity.Email},
	} {
		if strings.TrimSpace(field.value) != "" {
			return field.name, field.value
		}
	}
	return "", ""
}

func (r *AuthRPC) Save(req AuthSave, _ *struct{}) error {
	ctx := context.Background()
	if req.Create && r.Connections != nil {
		c := r.Connections
		c.mu.Lock()
		if a := c.attempts[req.Attempt]; a != nil && a.ready != nil {
			ready := a.ready
			c.mu.Unlock()
			<-ready
			c.mu.Lock()
		}
		defer c.mu.Unlock()
		a := c.attempts[req.Attempt]
		if req.Attempt != "" && (a == nil || a.status != contract.ConnectPending || a.saved || !c.now().Before(a.expires) || a.generation != c.proxy.generation.Load()) {
			return errors.New("account completion rejected")
		}
		if a != nil && a.account != "" {
			records, err := r.Store.ListProxyAuths(ctx)
			if err != nil {
				return errors.New("proxy store unavailable")
			}
			var saved private.ProxyAuth
			for _, record := range records {
				if record.ID == a.proxyID {
					saved = record
					break
				}
			}
			kind, identity := rawCredentialIdentity(saved.Record)
			newKind, newIdentity := req.Identity.identity()
			if saved.ID == "" || saved.Provider != req.Record.Provider || req.Record.ID != a.proxyID || identity == "" || kind != newKind || identity != newIdentity {
				a.status, a.reason = contract.ConnectFailed, contract.ConnectWrongAccount
				return errors.New("account completion rejected")
			}
			if err := r.Store.SaveProxyAuth(ctx, req.Record, false); err != nil {
				return errors.New("proxy store unavailable")
			}
			a.saved = true
			return nil
		}
	}
	if err := r.Store.SaveProxyAuth(ctx, req.Record, req.Create); err != nil {
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
	r.Connections.mu.Lock()
	defer r.Connections.mu.Unlock()
	for _, attempt := range r.Connections.attempts {
		if attempt.state == req.State {
			models := r.Connections.proxy.planModels[attempt.provider]
			if len(models) == 0 {
				return errors.New("plan model unavailable")
			}
			out.Prefix = "plan_" + string(attempt.provider)
			selected := r.Connections.proxy.planDefaults[attempt.provider]
			if selected == "" {
				selected = models[0].ID
			}
			out.Model = out.Prefix + "/" + planProxyModel(attempt.provider, selected)
			out.Accepted = true
			return nil
		}
	}
	return errors.New("account completion unavailable")
}

func serveAuthRPC(conn io.ReadWriteCloser, st AuthStore, complete func(context.Context, OAuthCompletion) error, connections *Connections) {
	server := rpc.NewServer()
	_ = server.RegisterName("Auth", &AuthRPC{Store: st, Complete: complete, Connections: connections})
	server.ServeConn(conn)
}

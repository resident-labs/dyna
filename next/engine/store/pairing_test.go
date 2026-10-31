package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestPairLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(100000, 0)
	newCode := func() string {
		t.Helper()
		code, err := s.NewPairCode(ctx, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 10 {
			t.Fatal(code)
		}
		return code
	}
	reject := func(code string, at time.Time) {
		t.Helper()
		_, _, err := s.RedeemPairCode(ctx, code, "phone", nil, at)
		if !errors.Is(err, ErrPair) {
			t.Fatalf("expected rejection: %v", err)
		}
	}
	old := newCode()
	code := newCode()
	reject(old, now)
	for i := 0; i < 4; i++ {
		reject("incorrect", now)
	}
	reject(code, now)
	code = newCode()
	reject(code, now.Add(5*time.Minute))
	code = newCode()
	token, d, err := s.RedeemPairCode(ctx, code, "phone", []contract.Scope{contract.ScopeRead}, now)
	if err != nil {
		t.Fatal(err)
	}
	reject(code, now)
	hash := sha256.Sum256([]byte(token))
	got, ok, err := s.AuthenticateDevice(ctx, hash, now)
	if err != nil || !ok || got.ID != d.ID || len(got.Scopes) != 1 {
		t.Fatalf("authentication: %+v %v %v", got, ok, err)
	}
	found, err := s.SetDeviceScopes(ctx, d.ID, []contract.Scope{contract.ScopeOperate})
	if err != nil || !found {
		t.Fatal(found, err)
	}
	list, err := s.ListDevices(ctx, now)
	if err != nil || len(list) != 1 || list[0].Scopes[0] != contract.ScopeOperate {
		t.Fatal(list, err)
	}
	found, err = s.RevokeDevice(ctx, d.ID, now)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	_, ok, err = s.AuthenticateDevice(ctx, hash, now)
	if err != nil || ok {
		t.Fatal(ok, err)
	}
}

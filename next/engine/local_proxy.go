package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/resident-labs/dyna/contract"
)

// ProxyKey returns the owner's proxy key. Its method table row keeps it to the owner socket.
func (s *Service) ProxyKey(ctx context.Context, caller contract.Principal, req contract.ProxyKeyRequest) (contract.ProxyKeyResponse, *contract.Error) {
	var out contract.ProxyKeyResponse
	if problem := deviceOwner(caller); problem != nil {
		return out, problem
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return out, failure(err)
	}
	key, err := s.store.SetLocalProxyKey(ctx, "dyna-proxy-"+hex.EncodeToString(secret[:]), req.Reset)
	if err != nil {
		return out, failure(err)
	}
	return contract.ProxyKeyResponse{Key: key}, nil
}

func (s *Service) ProxyUsage(ctx context.Context, caller contract.Principal, _ contract.ProxyUsageRequest) (contract.ProxyUsageResponse, *contract.Error) {
	var out contract.ProxyUsageResponse
	if problem := deviceOwner(caller); problem != nil {
		return out, problem
	}
	models, err := s.store.LocalUsage(ctx)
	if err != nil {
		return out, failure(err)
	}
	return contract.ProxyUsageResponse{Models: models}, nil
}

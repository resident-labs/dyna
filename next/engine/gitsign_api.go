package engine

import (
	"context"
	"errors"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/gitsign"
	"github.com/resident-labs/dyna/engine/store"
)

// signingKey returns the engine's git signing key, made on first use. It is nil when the engine has no data directory, as in
// a test without one, and then boxes get no signing.
func (s *Service) signingKey() (*gitsign.Key, error) {
	s.signing.Lock()
	defer s.signing.Unlock()
	if s.signing.key == nil && s.layout.Data != "" {
		key, err := gitsign.Open(s.layout.Data)
		if err != nil {
			return nil, err
		}
		s.signing.key = key
	}
	return s.signing.key, nil
}

// gitIdentity is the committer identity a room's boxes get: the room's GitHub account when it has one, else the engine's.
func (s *Service) gitIdentity(account store.GitHubIdentity) (name, email string) {
	if account.Login != "" {
		return account.Name, account.Email
	}
	return s.gitName, s.gitEmail
}

// GitSign signs a commit or tag object for the box's git, as the identity the engine configured for that box.
func (s *Service) GitSign(ctx context.Context, caller contract.Principal, req contract.GitSignRequest) (contract.GitSignResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.GitSignResponse{}, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.GitSignResponse{}, failure(err)
	}
	account, err := s.store.GitHubAccount(ctx, session.Room)
	if err != nil {
		return contract.GitSignResponse{}, failure(err)
	}
	on, err := s.store.RoomGitSigning(ctx, session.Room)
	if errors.Is(err, store.ErrRoomNotFound) {
		return contract.GitSignResponse{}, invalid("this room is closed or being deleted, so the engine signs nothing")
	}
	if err != nil {
		return contract.GitSignResponse{}, failure(err)
	}
	if !on {
		return contract.GitSignResponse{}, invalid("git signing is off for this room, so the engine signs nothing. The owner turns it on with the room's gitSigning setting (`dyna room signing ROOM on`, or Settings for the room). Until then commit unsigned: git -c commit.gpgsign=false -c tag.gpgsign=false ..., and unset the signing config this box started with")
	}
	name, email := s.gitIdentity(account)
	key, err := s.signingKey()
	if err != nil {
		return contract.GitSignResponse{}, failure(err)
	}
	if key == nil {
		return contract.GitSignResponse{}, invalid("this engine has no signing key, so it signs nothing. Commit without signing: git -c commit.gpgsign=false commit")
	}
	if err := gitsign.Check(req.Payload, name, email); err != nil {
		return contract.GitSignResponse{}, invalid(err.Error())
	}
	signature, err := key.Sign(req.Payload)
	if err != nil {
		return contract.GitSignResponse{}, failure(err)
	}
	return contract.GitSignResponse{Signature: signature}, nil
}

// SigningKey is the owner's way to read the public key to add on GitHub.
func (s *Service) SigningKey(context.Context, contract.Principal, contract.SigningKeyRequest) (contract.SigningKeyResponse, *contract.Error) {
	key, err := s.signingKey()
	if err != nil {
		return contract.SigningKeyResponse{}, failure(err)
	}
	if key == nil {
		return contract.SigningKeyResponse{}, invalid("this engine has no data directory for a signing key")
	}
	return contract.SigningKeyResponse{PublicKey: key.PublicKey()}, nil
}

// boxSigningKey is the public key line a room's box signs with, or empty while the room's signing is off.
func (s *Service) boxSigningKey(ctx context.Context, room contract.RoomID) (string, *contract.Error) {
	on, err := s.store.RoomGitSigning(ctx, room)
	if errors.Is(err, store.ErrRoomNotFound) {
		return "", nil
	}
	if err != nil {
		return "", failure(err)
	}
	if !on {
		return "", nil
	}
	key, err := s.signingKey()
	if err != nil {
		return "", failure(err)
	}
	if key == nil {
		return "", nil
	}
	return key.PublicKey(), nil
}

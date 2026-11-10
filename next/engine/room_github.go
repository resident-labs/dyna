package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/githubaccount"
)

func (s *Service) RoomGitHub(ctx context.Context, caller contract.Principal, req contract.RoomGitHubRequest) (contract.RoomGitHubResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.RoomGitHubResponse{}, forbidden()
	}
	if s.githubAccounts == nil {
		return contract.RoomGitHubResponse{}, invalid("GitHub accounts are unavailable")
	}
	room, _, err := s.store.ResolveRoom(ctx, string(req.Room))
	if err != nil {
		return contract.RoomGitHubResponse{}, invalid("unknown room")
	}
	if req.Clear && (req.Token != "" || req.Name != "" || req.Email != "") {
		return contract.RoomGitHubResponse{}, invalid("clear cannot include account details")
	}
	if req.Token == "" && (req.Name != "" || req.Email != "") {
		return contract.RoomGitHubResponse{}, invalid("account details require a token on stdin")
	}
	s.githubChange.Lock()
	defer s.githubChange.Unlock()
	account, err := s.store.GitHubAccount(ctx, room)
	if err != nil {
		return contract.RoomGitHubResponse{}, failure(err)
	}
	old := account
	if req.Clear {
		account = githubaccount.Account{}
		if err := s.store.SetGitHubAccount(ctx, room, account); err != nil {
			return contract.RoomGitHubResponse{}, failure(err)
		}
	} else if req.Token != "" {
		if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Email) == "" {
			return contract.RoomGitHubResponse{}, invalid("name and email are required")
		}
		login, err := githubaccount.Validate(ctx, &http.Client{Timeout: 30 * time.Second}, "https://api.github.com", req.Token)
		if err != nil {
			return contract.RoomGitHubResponse{}, failure(err)
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return contract.RoomGitHubResponse{}, failure(err)
		}
		account = githubaccount.Account{Login: login, Name: req.Name, Email: req.Email, Credential: contract.RoomID(hex.EncodeToString(random[:]))}
		if err := s.githubAccounts.Tokens.Write(account.Credential, req.Token); err != nil {
			return contract.RoomGitHubResponse{}, failure(err)
		}
		if err := s.store.SetGitHubAccount(ctx, room, account); err != nil {
			_ = s.githubAccounts.Tokens.Clear(account.Credential)
			return contract.RoomGitHubResponse{}, failure(err)
		}
	}
	if (req.Clear || req.Token != "") && old.Credential != "" {
		_ = s.githubAccounts.Tokens.Clear(old.Credential)
	}
	return contract.RoomGitHubResponse{Login: account.Login, Name: account.Name, Email: account.Email}, nil
}

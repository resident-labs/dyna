// Package githubaccount resolves control-plane GitHub credentials without fallback
// for rooms that explicitly select an account. Tokens must never leave this package
// except to the control-plane client making the authenticated request.
package githubaccount

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/credential"
	"github.com/resident-labs/dyna/engine/store"
)

// Account is public metadata; it deliberately contains no credential.
type Account = store.GitHubIdentity

// Store supplies metadata and a repository's unique default room.
type Store interface {
	GitHubAccount(context.Context, contract.RoomID) (Account, error)
	RepoRoom(context.Context, contract.RepoName) (contract.RoomID, error)
}

// Resolver is shared by room-scoped and repository-scoped clients. An empty
// Account.Login means the room has not selected an account.
type Resolver struct {
	Store        Store
	Tokens       *Files
	DefaultToken string
}

func (r *Resolver) Room(ctx context.Context, room contract.RoomID) (string, string, error) {
	account, err := r.Store.GitHubAccount(ctx, room)
	if err != nil {
		return "", "", err
	}
	if account.Login == "" {
		return r.DefaultToken, "", nil
	}
	key := account.Credential
	if key == "" {
		key = room
	}
	token, err := r.Tokens.Read(key)
	if err != nil {
		return "", account.Login, fmt.Errorf("GitHub account %s of room %s: credential unavailable", account.Login, room)
	}
	return token, account.Login, nil
}

func (r *Resolver) Repo(ctx context.Context, repo contract.RepoName) (string, string, error) {
	room, err := r.Store.RepoRoom(ctx, repo)
	if err != nil {
		return "", "", err
	}
	return r.Room(ctx, room)
}

// Files is the shared control-plane credential store.
type Files = credential.Files

// Validate checks /user before metadata or a token may be saved. GitHub error
// bodies are intentionally not returned: a proxy may reflect credentials there.
func Validate(ctx context.Context, client *http.Client, endpoint, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("invalid GitHub token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/user", nil)
	if err != nil {
		return "", errors.New("invalid GitHub endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	// Do not follow redirects with a credential, including redirects to another host.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := noRedirect.Do(req)
	if err != nil {
		return "", errors.New("GitHub account validation unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub rejected account validation (HTTP %d)", response.StatusCode)
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&user); err != nil || user.Login == "" {
		return "", errors.New("invalid GitHub user response")
	}
	return user.Login, nil
}

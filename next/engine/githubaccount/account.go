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
	"os"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// Account is public metadata; it deliberately contains no credential.
type Account struct{ Login, Name, Email string }

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
	token, err := r.Tokens.Read(room)
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

// Files keeps credentials outside the database and replaces them atomically.
// Dir must be a private control-plane configuration directory, not a box mount.
type Files struct{ Dir string }

func (f *Files) path(room contract.RoomID) string {
	// Encoding the ID avoids interpreting even an invalid ID as a path.
	return filepath.Join(f.Dir, fmt.Sprintf("%x.token", string(room)))
}

func (f *Files) Read(room contract.RoomID) (string, error) {
	data, err := os.ReadFile(f.path(room))
	if err != nil {
		return "", errors.New("credential unavailable")
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("credential unavailable")
	}
	return token, nil
}

func (f *Files) Write(room contract.RoomID, token string) error {
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return errors.New("invalid GitHub token")
	}
	if err := os.MkdirAll(f.Dir, 0700); err != nil {
		return errors.New("cannot create credential directory")
	}
	file, err := os.CreateTemp(f.Dir, ".github-*")
	if err != nil {
		return errors.New("cannot create credential file")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := io.WriteString(file, token); err != nil {
		return errors.New("cannot write credential file")
	}
	if err := file.Sync(); err != nil {
		return errors.New("cannot sync credential file")
	}
	if err := file.Close(); err != nil {
		return errors.New("cannot close credential file")
	}
	if err := os.Rename(file.Name(), f.path(room)); err != nil {
		return errors.New("cannot replace credential file")
	}
	return nil
}

func (f *Files) Clear(room contract.RoomID) error {
	err := os.Remove(f.path(room))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot remove credential file")
	}
	return nil
}

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

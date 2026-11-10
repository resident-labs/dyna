package git

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type mirrorIdentity struct {
	Login      string `json:"login"`
	Generation string `json:"generation"`
}

func (m *Mirror) identityMatches(path, login string) (bool, error) {
	if _, err := os.Stat(path + ".requires-fetch"); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	data, err := os.ReadFile(filepath.Join(path, box.GitIdentityMarker))
	if errors.Is(err, os.ErrNotExist) {
		return login == "", nil
	}
	if err != nil {
		return false, err
	}
	var identity mirrorIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return false, nil
	}
	return strings.EqualFold(identity.Login, login), nil
}

// ensureIdentity never serves objects left by another identity. The pending
// marker survives failed refills, including when the next caller is the engine
// account whose older, unmarked mirrors remain compatible.
func (m *Mirror) ensureIdentity(ctx context.Context, repo contract.RepoName, login string) error {
	path, err := m.Layout.Git(repo)
	if err != nil {
		return err
	}
	matches, err := m.identityMatches(path, login)
	if err != nil || matches {
		return err
	}
	return m.fetches.do(ctx, repo, func(ctx context.Context) error {
		matches, err := m.identityMatches(path, login)
		if err != nil || matches {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(path+".requires-fetch", nil, 0600); err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		m.fetched.Delete(repo)
		return m.fetch(ctx, repo, path)
	})
}

func (m *Mirror) recordIdentity(login, path string) error {
	previous, markerErr := os.ReadFile(filepath.Join(path, box.GitIdentityMarker))
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return markerErr
	}
	_, pendingErr := os.Stat(path + ".requires-fetch")
	if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		return pendingErr
	}
	// Ordinary mirrors retain their legacy, unmarked provenance until an identity
	// change actually discards them. A normal fetch must not reset host caches.
	if login == "" && errors.Is(markerErr, os.ErrNotExist) && errors.Is(pendingErr, os.ErrNotExist) {
		return nil
	}
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return err
	}
	identity := mirrorIdentity{Login: login, Generation: hex.EncodeToString(generation[:])}
	if markerErr == nil {
		var old mirrorIdentity
		if json.Unmarshal(previous, &old) == nil && strings.EqualFold(old.Login, login) && old.Generation != "" {
			identity.Generation = old.Generation
		}
	}
	data, _ := json.Marshal(identity)
	if err := os.WriteFile(filepath.Join(path, box.GitIdentityMarker), data, 0600); err != nil {
		return err
	}
	if err := os.Remove(path + ".requires-fetch"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/resident-labs/dyna/engine/store"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
)

// legacy/shim: import M1's file token store once, then remove the imported files.
func (s *proxyStore) importLegacy(ctx context.Context, dir string) error {
	old := sdkauth.NewFileTokenStore()
	old.SetBaseDir(dir)
	auths, err := old.List(ctx)
	if err != nil {
		return errors.New("legacy proxy import failed")
	}
	sort.Slice(auths, func(i, j int) bool {
		if auths[i].Provider != auths[j].Provider {
			return auths[i].Provider < auths[j].Provider
		}
		return auths[i].ID < auths[j].ID
	})
	paths := make(map[string]bool)
	var callbacks []string
	records := make([]store.ImportedProxyAuth, 0, len(auths))
	counts := make(map[string]int)
	for _, auth := range auths {
		path := auth.Attributes["path"]
		if path == "" {
			return errors.New("legacy proxy import failed")
		}
		paths[filepath.Clean(path)] = true
		delete(auth.Attributes, "path")
		delete(auth.Attributes, "source")
		delete(auth.Attributes, "source_backend")
		raw, err := json.Marshal(auth)
		if err != nil {
			return errors.New("legacy proxy import failed")
		}
		record := store.ImportedProxyAuth{Auth: store.ProxyAuth{ID: auth.ID, Provider: auth.Provider, Record: raw, UpdatedAt: time.Now().UnixMilli()}}
		var name string
		switch auth.Provider {
		case "claude":
			record.Provider, name = "anthropic", "Claude"
		case "codex":
			record.Provider, name = "openai", "ChatGPT"
		case "kimi":
			record.Provider, name = "kimi", "Kimi"
		case "xai":
			record.Provider, name = "xai", "xAI"
		}
		if name != "" {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return errors.New("legacy proxy import failed")
			}
			record.AccountID = hex.EncodeToString(id[:])
			counts[name]++
			record.Label = fmt.Sprintf("Imported %s %d", name, counts[name])
		}
		records = append(records, record)
	}
	// The upstream file reader silently skips invalid files. Refuse to discard one.
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".oauth-") {
			callbacks = append(callbacks, path)
			return nil
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".json") && !paths[filepath.Clean(path)] {
			return errors.New("unreadable credential")
		}
		return nil
	})
	if err != nil {
		return errors.New("legacy proxy import failed")
	}
	if err := s.client.Call("Auth.Import", records, &struct{}{}); err != nil {
		return errors.New("legacy proxy import failed")
	}
	for path := range paths {
		callbacks = append(callbacks, path)
	}
	for _, path := range callbacks {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("legacy proxy cleanup failed")
		}
	}
	return nil
}

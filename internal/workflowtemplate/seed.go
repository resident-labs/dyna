package workflowtemplate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dyna-agent/examples"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// SeedResult is the JSON contract for a managed seed pass.
type SeedResult struct {
	Written   []string `json:"written"`
	Updated   []string `json:"updated"`
	Unchanged []string `json:"unchanged"`
	Preserved []string `json:"preserved"`
}

type managedManifest struct {
	Files map[string]string `json:"files"`
}

// SeedShipped installs or refreshes the embedded examples/ starters into the
// user registry using the managed-seeding policy.
func (s Store) SeedShipped(ctx context.Context, c Catalog) (SeedResult, error) {
	return s.Seed(ctx, c, examples.FS)
}

// Seed installs or refreshes starters from fsys into the user registry.
//
// Policy, applied independently per safe basename:
//   - missing dest: write the bundled bytes and record their hash
//   - dest bytes equal the current bundle: already current; refresh the record
//   - dest bytes equal the last seeded hash: unchanged managed copy; update
//   - anything else, including an unknown existing file: leave it untouched
//
// Writes are atomic. The catalog never inspects <work-dir>/examples.
func (s Store) Seed(ctx context.Context, c Catalog, fsys fs.FS) (SeedResult, error) {
	result := SeedResult{
		Written:   []string{},
		Updated:   []string{},
		Unchanged: []string{},
		Preserved: []string{},
	}
	db, err := s.db()
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(c.RegistryDir) == "" {
		c.RegistryDir = DefaultRegistryDir()
	}
	registry, err := filepath.Abs(c.RegistryDir)
	if err != nil {
		return result, err
	}
	c.RegistryDir = filepath.Clean(registry)
	if err := os.MkdirAll(c.RegistryDir, 0o755); err != nil {
		return result, err
	}
	unlock, err := tryLockDir(c.RegistryDir)
	if err != nil {
		return result, err
	}
	defer unlock()

	starters, err := bundledStarters(fsys)
	if err != nil {
		return result, err
	}
	if err := importSeedManifest(ctx, db, c.RegistryDir); err != nil {
		return result, err
	}
	manifest, err := readSeedRecord(ctx, db, c.RegistryDir)
	if err != nil {
		return result, err
	}
	// File I/O uses the registry lock, never SQLite's writer lock. If a
	// crash precedes the row save, matching bundle bytes repair the hash.
	next := managedManifest{Files: map[string]string{}}
	for _, starter := range starters {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		outcome, hash, err := c.seedOne(starter, manifest.Files[starter.Name])
		if err != nil {
			return result, err
		}
		switch outcome {
		case "written":
			result.Written = append(result.Written, starter.Name)
		case "updated":
			result.Updated = append(result.Updated, starter.Name)
		case "unchanged":
			result.Unchanged = append(result.Unchanged, starter.Name)
		case "preserved":
			result.Preserved = append(result.Preserved, starter.Name)
			hash = manifest.Files[starter.Name]
		}
		if hash != "" {
			next.Files[starter.Name] = hash
		}
	}
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return saveSeedRecord(ctx, conn, c.RegistryDir, next)
	})
	if err != nil {
		return result, err
	}
	if err := mirrorSeedManifest(context.WithoutCancel(ctx), db, c.RegistryDir); err != nil {
		return result, fmt.Errorf("seed committed, but rollback manifest failed: %w", err)
	}
	return result, nil
}

type bundledStarter struct {
	Name string
	Data []byte
	Hash string
}

func bundledStarters(fsys fs.FS) ([]bundledStarter, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []bundledStarter
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".js" {
			continue
		}
		name, err := NormalizeName(strings.TrimSuffix(entry.Name(), ".js"))
		if err != nil || name+".js" != entry.Name() {
			continue
		}
		data, err := fs.ReadFile(fsys, entry.Name())
		if err != nil {
			return nil, err
		}
		if err := ValidateSource(data); err != nil {
			continue
		}
		out = append(out, bundledStarter{Name: name, Data: data, Hash: contentHash(data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (c Catalog) seedOne(starter bundledStarter, lastHash string) (string, string, error) {
	path := filepath.Join(c.RegistryDir, starter.Name+".js")
	if !pathInsideDir(c.RegistryDir, path) {
		return "preserved", "", nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", "", err
		}
		if err := replaceRegularFile(path, starter.Data, false); err != nil {
			return "", "", err
		}
		return "written", starter.Hash, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "preserved", "", nil
	}
	current, err := readRegularFile(path)
	if err != nil {
		if isSymlinkError(err) {
			return "preserved", "", nil
		}
		return "", "", err
	}
	currentHash := contentHash(current)
	if currentHash == starter.Hash {
		return "unchanged", starter.Hash, nil
	}
	if lastHash != "" && currentHash == lastHash {
		if err := replaceRegularFile(path, starter.Data, true); err != nil {
			return "", "", err
		}
		return "updated", starter.Hash, nil
	}
	return "preserved", "", nil
}

// TODO: legacy/shim remove later — decodes manifests written by the previous release; remove with requirement 31.
func decodeManagedManifest(data []byte) (managedManifest, error) {
	if len(data) > MaxSourceBytes {
		return managedManifest{}, fmt.Errorf("template seed manifest exceeds %d bytes", MaxSourceBytes)
	}
	var manifest managedManifest
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Files == nil {
		return managedManifest{Files: map[string]string{}}, nil
	}
	return manifest, nil
}

// TODO: legacy/shim remove later — locates the previous release rollback manifest; remove with requirement 31.
func manifestPath(dir string) string {
	return filepath.Join(dir, ManagedManifestName)
}

// TODO: legacy/shim remove later — encodes the previous release rollback manifest; remove with requirement 31.
func marshalManifest(manifest managedManifest) ([]byte, error) {
	if manifest.Files == nil {
		manifest.Files = map[string]string{}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

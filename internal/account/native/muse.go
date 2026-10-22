package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/account/store"
)

const museAuthSchema = "muse/auth-file@1"
const museAuthMaxBytes = 4 << 20

type museManager struct{ host Host }

func init() { register("muse", func(h Host) Manager { return &museManager{host: h} }) }

func (*museManager) Harness() string { return "muse" }

func (*museManager) Caps() Caps {
	return Caps{
		Activate: true, IsolatedLogin: true,
		Scope:         "Muse credentials for this host user",
		RunningEffect: "Running Muse hosts may keep the old account. Restart them for a certain switch.",
	}
}

func (m *museManager) authPath(root string) (string, error) {
	if root != "" {
		if !filepath.IsAbs(root) {
			return "", ErrUnsupported
		}
		return filepath.Join(root, ".config", "muse", "auth.json"), nil
	}
	if backend := m.host.Getenv("TBH_CREDENTIAL_BACKEND"); backend != "" && backend != "file" {
		return "", ErrUnsupported
	}
	// An environment key wins over the native file. Do not capture or switch a
	// hidden file account and then report it as the effective account.
	if m.host.Getenv("META_API_KEY") != "" {
		return "", ErrUnsupported
	}
	config := m.host.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		home := m.host.HomeDir()
		if home == "" || !filepath.IsAbs(home) {
			return "", ErrUnsupported
		}
		config = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(config) {
		return "", ErrUnsupported
	}
	// MUSE_AUTH_PATH is established for the launcher, not the native host.
	return filepath.Join(config, "muse", "auth.json"), nil
}

func museReadAuth(path string) (map[string]json.RawMessage, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, ErrNotSignedIn
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("Muse credentials could not be read safely.")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("Muse credentials could not be read.")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, museAuthMaxBytes+1))
	if err != nil {
		return nil, errors.New("Muse credentials could not be read.")
	}
	var doc map[string]json.RawMessage
	if len(raw) > museAuthMaxBytes || json.Unmarshal(raw, &doc) != nil || doc == nil {
		return nil, ErrSchema
	}
	var version int
	if json.Unmarshal(doc["schema_version"], &version) != nil || version != 1 {
		return nil, ErrSchema
	}
	return doc, nil
}

func museProviders(doc map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if _, ok := doc["providers"]; !ok {
		return map[string]json.RawMessage{}, nil
	}
	var providers map[string]json.RawMessage
	if json.Unmarshal(doc["providers"], &providers) != nil || providers == nil {
		return nil, ErrSchema
	}
	return providers, nil
}

func museMetaIdentity(raw json.RawMessage) (string, string, time.Time, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return "", "", time.Time{}, ErrSchema
	}
	var meta struct {
		Storage      string `json:"storage"`
		Mechanism    string `json:"mechanism"`
		APIKey       string `json:"api_key"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Email        string `json:"user_email"`
		ExpiresAt    *int64 `json:"expires_at"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return "", "", time.Time{}, ErrSchema
	}
	if meta.Storage != "" && meta.Storage != "file" {
		return "", "", time.Time{}, ErrUnsupported
	}
	if meta.APIKey == "" && meta.AccessToken == "" && meta.RefreshToken == "" {
		if meta.Mechanism == "oauth" {
			// Metadata without inline material can point to an OS secret store.
			return "", "", time.Time{}, ErrUnsupported
		}
		return "", "", time.Time{}, ErrNotSignedIn
	}
	email := strings.TrimSpace(meta.Email)
	if email == "" || strings.ContainsAny(email, "\r\n\x00") {
		return "", "", time.Time{}, ErrNoIdentity
	}
	var expiry time.Time
	if meta.ExpiresAt != nil && *meta.ExpiresAt > 0 {
		expiry = time.Unix(*meta.ExpiresAt, 0).UTC()
	}
	return "meta:email:" + strings.ToLower(email), email, expiry, nil
}

func (m *museManager) Capture(ctx context.Context, root string) ([]Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := m.authPath(root)
	if err != nil {
		return nil, err
	}
	doc, err := museReadAuth(path)
	if err != nil {
		return nil, err
	}
	providers, err := museProviders(doc)
	if err != nil {
		return nil, err
	}
	meta, ok := providers["meta"]
	if !ok {
		return nil, ErrNotSignedIn
	}
	return museCaptureMeta(meta)
}

func museCaptureMeta(meta json.RawMessage) ([]Captured, error) {
	identity, label, expiry, err := museMetaIdentity(meta)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]json.RawMessage{"metaCredential": meta})
	if err != nil {
		return nil, ErrSchema
	}
	return []Captured{{Identity: identity, Label: label, ExpiresAt: expiry,
		Credential: store.Credential{Schema: museAuthSchema, Payload: payload}}}, nil
}

// Lock the existing companion inode, never the replaceable auth file. This
// serializes Dyna writers; native lock interoperability remains unverified.
func museLock(ctx context.Context, path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("Muse credentials could not be locked safely.")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, errors.New("Muse credentials could not be locked.")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("Muse credentials could not be locked.")
	}
	timer := time.NewTicker(20 * time.Millisecond)
	defer timer.Stop()
	for {
		if err = ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, errors.New("Muse credentials could not be locked.")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *museManager) Activate(ctx context.Context, integration string, c store.Credential) (previous []Captured, err error) {
	if err := ctx.Err(); err != nil {
		return previous, err
	}
	if integration != "" || c.Schema != museAuthSchema || len(c.Payload) > museAuthMaxBytes {
		return previous, ErrSchema
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(c.Payload, &payload) != nil || len(payload) != 1 {
		return previous, ErrSchema
	}
	meta := payload["metaCredential"]
	if _, _, _, err := museMetaIdentity(meta); err != nil {
		return previous, err
	}
	path, err := m.authPath("")
	if err != nil {
		return previous, err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return previous, errors.New("Muse credential directory could not be created.")
	}
	lock, err := museLock(ctx, filepath.Join(dir, ".auth.json.lock"))
	if err != nil {
		return previous, err
	}
	defer lock.Close()
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	doc, err := museReadAuth(path)
	if errors.Is(err, ErrNotSignedIn) {
		doc = map[string]json.RawMessage{"schema_version": json.RawMessage("1")}
	} else if err != nil {
		return previous, err
	}
	providers, err := museProviders(doc)
	if err != nil {
		return previous, err
	}
	if current, ok := providers["meta"]; ok {
		// Do not overwrite a Keychain-backed or unidentified outgoing login.
		previous, err = museCaptureMeta(current)
		if err != nil && !errors.Is(err, ErrNotSignedIn) {
			return previous, err
		}
	}
	providers["meta"] = meta
	doc["providers"], err = json.Marshal(providers)
	if err != nil {
		return previous, ErrSchema
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil || len(raw) > museAuthMaxBytes {
		return previous, ErrSchema
	}
	f, err := os.CreateTemp(dir, ".auth.json-*")
	if err != nil {
		return previous, errors.New("Muse credentials could not be written.")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	if err == nil && ctx.Err() != nil {
		return previous, ctx.Err()
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return previous, errors.New("Muse credentials could not be written.")
	}
	d, err := os.Open(dir)
	if err != nil {
		return previous, ErrPartialActivation
	}
	defer d.Close()
	if d.Sync() != nil {
		return previous, ErrPartialActivation
	}
	return previous, nil
}

func (*museManager) Refresh(context.Context, Captured) (Captured, error) {
	return Captured{}, ErrUnsupported
}

func (m *museManager) LoginCommand(root, _ string) ([]string, []string, error) {
	if root != "" && !filepath.IsAbs(root) {
		return nil, nil, ErrUnsupported
	}
	executable := m.host.Executable
	if executable == "" {
		executable = "muse"
	}
	if root == "" {
		return []string{executable, "login"}, []string{"MUSE_NO_AUTO_UPDATE=1"}, nil
	}
	return []string{executable, "login"}, []string{
		"HOME=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(root, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(root, ".cache"),
		"TBH_CREDENTIAL_BACKEND=file", "META_API_KEY=",
		"MUSE_AUTH_PATH=" + filepath.Join(root, ".config", "muse", "auth.json"),
		"MUSE_NO_AUTO_UPDATE=1", "TBH_DISABLE_TELEMETRY=1",
	}, nil
}

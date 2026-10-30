package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	accountlabel "dyna-agent/internal/account/label"
	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/fsutil"
)

// NativeRuntimeManager supplies a native-file backend for harnesses without an
// account/native adapter. Grants stay in native files and in memory during swaps.
func NativeRuntimeManager(h string, host native.Host) (native.Manager, bool) {
	if m, ok := native.For(h, host); ok {
		return m, true
	}
	a, err := Adapter(h)
	if err != nil || len(a.AuthFiles) == 0 {
		return nil, false
	}
	return &nativeFilesManager{a, host}, true
}

type nativeFilesManager struct {
	adapter RuntimeAdapter
	host    native.Host
}

func (m *nativeFilesManager) Harness() string { return m.adapter.Harness }
func (m *nativeFilesManager) Caps() native.Caps {
	return native.Caps{Refresh: m.adapter.Harness == "grok" || m.adapter.Harness == "pi", Activate: true, IsolatedLogin: true, Scope: "Dyna's shared native harness home", RunningEffect: "New turns use the active native login after the harness gate drains."}
}
func (m *nativeFilesManager) root(root string) (string, error) {
	if root != "" {
		if !filepath.IsAbs(root) {
			return "", native.ErrUnsupported
		}
		return root, nil
	}
	key := ""
	switch m.adapter.Harness {
	case "grok":
		key = "GROK_HOME"
	case "pi":
		key = "PI_CODING_AGENT_DIR"
	}
	if key != "" {
		if v := m.host.Getenv(key); v != "" {
			if !filepath.IsAbs(v) {
				return "", native.ErrUnsupported
			}
			return v, nil
		}
	}
	return "", native.ErrUnsupported
}
func (m *nativeFilesManager) Capture(_ context.Context, root string) ([]native.Captured, error) {
	dir, err := m.root(root)
	if err != nil {
		return nil, err
	}
	files := map[string]json.RawMessage{}
	for _, name := range m.adapter.AuthFiles {
		path := filepath.Join(dir, name)
		if !inside(dir, path) {
			return nil, native.ErrUnsupported
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("native login file could not be read")
		}
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) > 4<<20 || !json.Valid(raw) {
			return nil, native.ErrSchema
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || len(object) == 0 || !nativeHasGrant(object) {
			continue
		}
		files[name] = raw
	}
	if len(files) == 0 {
		return nil, native.ErrNotSignedIn
	}
	payload, _ := json.Marshal(files)
	c := native.Captured{Identity: m.adapter.Harness + ":native", Label: m.adapter.Harness + " native login", Credential: store.Credential{Schema: m.adapter.Harness + "/native-files@1", Payload: payload}}
	// A Pi account holds one provider's login, so the provider part of the
	// stored identity names it. A home with several entries keeps the
	// placeholder: it cannot say which provider this account is.
	if m.adapter.Harness == "pi" {
		var providers map[string]json.RawMessage
		if json.Unmarshal(files["auth.json"], &providers) == nil && len(providers) == 1 {
			for id, entry := range providers {
				c.Identity = "pi:" + id
				c.Label = accountlabel.PiDefaultLabel(id, entry)
			}
		}
	}
	// A Grok OIDC login names its user, so it gets the stable identity that
	// usage and saved accounts use. Other logins keep the placeholder.
	if m.adapter.Harness == "grok" {
		c.Label = ""
		if identity, err := native.GrokNativeIdentity(c); err == nil {
			c.Identity = identity
			c.Label = native.CredentialLabel(c)
		}
	}
	return []native.Captured{c}, nil
}
func (m *nativeFilesManager) Activate(ctx context.Context, _ string, c store.Credential) ([]native.Captured, error) {
	if c.Schema != m.adapter.Harness+"/native-files@1" {
		return nil, native.ErrSchema
	}
	var files map[string]json.RawMessage
	if json.Unmarshal(c.Payload, &files) != nil {
		return nil, native.ErrSchema
	}
	root, err := m.root("")
	if err != nil {
		return nil, err
	}
	old, captureErr := m.Capture(ctx, "")
	if captureErr != nil && !errors.Is(captureErr, native.ErrNotSignedIn) {
		return nil, captureErr
	}
	for name, raw := range files {
		allowed := false
		for _, known := range m.adapter.AuthFiles {
			allowed = allowed || name == known
		}
		if !allowed || !json.Valid(raw) {
			return old, native.ErrSchema
		}
	}
	for name, raw := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return old, err
		}
		if err := fsutil.WriteFile(path, raw, 0600, true); err != nil {
			return old, native.ErrPartialActivation
		}
	}
	return old, nil
}
func (m *nativeFilesManager) Refresh(ctx context.Context, c native.Captured) (native.Captured, error) {
	switch m.adapter.Harness {
	case "grok":
		return native.RefreshGrokSaved(ctx, c)
	case "pi":
		return m.renewPi(ctx, c)
	}
	return c, native.ErrUnsupported
}

// RefreshActive runs inside Accounts.RefreshCredentials, which holds the switch
// gate and account lock. Checkpoint the rotated grant before the native write.
func (m *nativeFilesManager) RefreshActive(ctx context.Context, c native.Captured, checkpoint func(native.Captured) error) (native.Captured, error) {
	if checkpoint == nil || (m.adapter.Harness != "grok" && m.adapter.Harness != "pi") {
		return c, native.ErrUnsupported
	}
	next, err := m.Refresh(ctx, c)
	if err != nil {
		return next, err
	}
	if next.Identity != c.Identity || next.Integration != c.Integration {
		return next, native.ErrNoIdentity
	}
	if err = checkpoint(next); err != nil {
		return next, err
	}
	if m.adapter.Harness == "pi" {
		// refreshPi wrote the rotated entry under Pi's own lock, so a second
		// native write here would race its next writer.
		return next, nil
	}
	_, err = m.Activate(ctx, next.Integration, next.Credential)
	return next, err
}
func (m *nativeFilesManager) LoginCommand(root, _ string) ([]string, []string, error) {
	home, err := m.root(root)
	if err != nil {
		return nil, nil, err
	}
	if m.adapter.LoginArgs == nil {
		return nil, nil, native.ErrUnsupported
	}
	exe := m.host.Executable
	if exe == "" {
		exe = m.adapter.Binary
	}
	argv := m.adapter.LoginArgs(exe, home)
	env := []string{}
	if m.adapter.NativeEnv != nil {
		for key, value := range m.adapter.NativeEnv(home) {
			if key == "HOME" || strings.HasPrefix(key, "XDG_") {
				return nil, nil, native.ErrUnsupported
			}
			env = append(env, key+"="+value)
		}
	}
	return argv, env, nil
}

func nativeHasGrant(value any) bool {
	switch object := value.(type) {
	case map[string]json.RawMessage:
		for key, raw := range object {
			var value any
			if json.Unmarshal(raw, &value) == nil {
				if nativeGrantValue(key, value) {
					return true
				}
			}
		}
	case map[string]any:
		for key, value := range object {
			if nativeGrantValue(key, value) {
				return true
			}
		}
	}
	return false
}
func nativeGrantValue(key string, value any) bool {
	if text, ok := value.(string); ok && text != "" {
		switch strings.ToLower(key) {
		case "key", "api_key", "apikey", "access", "refresh", "access_token", "refresh_token", "token", "auth_token":
			return true
		}
	}
	return nativeHasGrant(value)
}

func (m *nativeFilesManager) ProbeSaved(ctx context.Context, c native.Captured) (store.Observation, error) {
	if m.adapter.Harness == "pi" {
		return native.ProbePiSaved(ctx, c)
	}
	if m.adapter.Harness != "grok" {
		return store.Observation{}, native.ErrUnsupported
	}
	return native.ProbeGrokSaved(ctx, c)
}

func (m *nativeFilesManager) SupportsCredentialRefresh(c native.Captured) bool {
	switch m.adapter.Harness {
	case "grok":
		return native.GrokRefreshSupported(c)
	case "pi":
		return native.PiOAuthGrantUsable(c)
	}
	return false
}

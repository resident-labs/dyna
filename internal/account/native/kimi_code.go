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
	"dyna-agent/internal/fsutil"
	"github.com/pelletier/go-toml/v2"
)

const (
	kimiCodeSchema       = "kimi-code/native-files@1"
	kimiCodeIdentity     = "kimi-code:native"
	kimiCodeMaxFileBytes = 4 << 20
)

type kimiCodeManager struct {
	host     Host
	client   claudeHTTPClient
	tokenURL string
	now      func() time.Time
}

type kimiCodeOAuthRef struct {
	Storage   string `toml:"storage"`
	Key       string `toml:"key"`
	OAuthHost string `toml:"oauthHost"`
}

type kimiCodeProvider struct {
	Type    string            `toml:"type"`
	BaseURL string            `toml:"base_url"`
	OAuth   *kimiCodeOAuthRef `toml:"oauth"`
}

type kimiCodeConfig struct {
	Providers map[string]kimiCodeProvider `toml:"providers"`
}

type kimiCodeToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

type kimiCodeLogin struct {
	config    json.RawMessage
	token     json.RawMessage
	tokenPath string
	baseURL   string
	oauthHost string
	tokenInfo kimiCodeToken
	expiresAt time.Time
}

func init() {
	register("kimi-code", func(h Host) Manager { return &kimiCodeManager{host: h} })
}

func (*kimiCodeManager) Harness() string { return "kimi-code" }

func (*kimiCodeManager) Caps() Caps {
	return Caps{
		Activate: true, IsolatedLogin: true, Refresh: true,
		Scope:         "Dyna’s shared Kimi Code home",
		RunningEffect: "New turns use the active native login after the harness gate drains.",
	}
}

func (m *kimiCodeManager) root(root string) (string, error) {
	if root != "" {
		if !filepath.IsAbs(root) {
			return "", ErrUnsupported
		}
		return filepath.Clean(root), nil
	}
	if configured := m.host.Getenv("KIMI_CODE_HOME"); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", ErrUnsupported
		}
		return filepath.Clean(configured), nil
	}
	home := m.host.HomeDir()
	if home == "" || !filepath.IsAbs(home) {
		return "", ErrUnsupported
	}
	return filepath.Join(home, ".kimi-code"), nil
}

// kimiCodePath validates a native relative path and every existing component.
// It never follows a symlink into another credential tree.
func kimiCodePath(root, relative string) (string, error) {
	if !filepath.IsAbs(root) || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == "." || relative == "" {
		return "", errAccountPathForKimi()
	}
	path := filepath.Join(root, relative)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errAccountPathForKimi()
	}
	current := root
	info, err := os.Lstat(current)
	if err != nil && !os.IsNotExist(err) || err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return "", errAccountPathForKimi()
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err = os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errAccountPathForKimi()
		}
	}
	return path, nil
}

func errAccountPathForKimi() error {
	return errors.New("The Kimi Code account path is unsafe.")
}

func readKimiCodeFile(root, relative string) ([]byte, error) {
	path, err := kimiCodePath(root, relative)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, ErrNotSignedIn
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("Kimi Code account data could not be read safely.")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("Kimi Code account data could not be read.")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, kimiCodeMaxFileBytes+1))
	if err != nil || len(raw) > kimiCodeMaxFileBytes {
		return nil, ErrSchema
	}
	return raw, nil
}

func kimiCodeTokenFile(key string) (string, error) {
	name := ""
	switch {
	case key == "kimi-code" || key == "oauth/kimi-code":
		name = "kimi-code"
	case strings.HasPrefix(key, "oauth/"):
		name = strings.TrimPrefix(key, "oauth/")
	case !strings.Contains(key, "/"):
		name = key
	default:
		return "", ErrSchema
	}
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") || filepath.Base(name) != name || strings.ContainsAny(name, "\r\n\x00") {
		return "", ErrSchema
	}
	return filepath.Join("credentials", name+".json"), nil
}

func parseKimiCodeConfig(raw []byte) (kimiCodeProvider, error) {
	var config kimiCodeConfig
	if toml.Unmarshal(raw, &config) != nil {
		return kimiCodeProvider{}, ErrSchema
	}
	provider, ok := config.Providers["managed:kimi-code"]
	if !ok {
		return kimiCodeProvider{}, ErrNotSignedIn
	}
	if provider.Type != "kimi" || provider.OAuth == nil || provider.OAuth.Storage != "file" {
		return kimiCodeProvider{}, ErrUnsupported
	}
	if _, err := kimiCodeTokenFile(provider.OAuth.Key); err != nil {
		return kimiCodeProvider{}, err
	}
	if provider.BaseURL == "" {
		provider.BaseURL = "https://api.kimi.com/coding/v1"
	}
	return provider, nil
}

func parseKimiCodeToken(raw []byte) (kimiCodeToken, error) {
	var token kimiCodeToken
	if json.Unmarshal(raw, &token) != nil {
		return token, ErrSchema
	}
	for _, value := range []string{token.AccessToken, token.RefreshToken} {
		if value == "" || len(value) > 64<<10 || strings.ContainsAny(value, "\r\n\x00") {
			return token, ErrSchema
		}
	}
	if token.ExpiresAt <= 0 {
		return token, ErrSchema
	}
	return token, nil
}

func readKimiCodeLogin(root string) (kimiCodeLogin, error) {
	var login kimiCodeLogin
	config, err := readKimiCodeFile(root, "config.toml")
	if err != nil {
		return login, err
	}
	provider, err := parseKimiCodeConfig(config)
	if err != nil {
		return login, err
	}
	login.tokenPath, err = kimiCodeTokenFile(provider.OAuth.Key)
	if err != nil {
		return login, err
	}
	tokenRaw, err := readKimiCodeFile(root, login.tokenPath)
	if err != nil {
		return login, err
	}
	token, err := parseKimiCodeToken(tokenRaw)
	if err != nil {
		return login, err
	}
	login.config = append(json.RawMessage(nil), config...)
	login.token = append(json.RawMessage(nil), tokenRaw...)
	login.baseURL = provider.BaseURL
	login.oauthHost = provider.OAuth.OAuthHost
	login.tokenInfo = token
	login.expiresAt = time.Unix(token.ExpiresAt, 0).UTC()
	return login, nil
}

func packKimiCodeLogin(login kimiCodeLogin) (store.Credential, error) {
	if len(login.config) == 0 || len(login.token) == 0 || login.tokenPath == "" {
		return store.Credential{}, ErrSchema
	}
	payload, err := json.Marshal(map[string]string{
		"config.toml":   string(login.config),
		login.tokenPath: string(login.token),
	})
	if err != nil || len(payload) > 2*kimiCodeMaxFileBytes {
		return store.Credential{}, ErrSchema
	}
	return store.Credential{Schema: kimiCodeSchema, Payload: payload}, nil
}

func unpackKimiCodeLogin(c store.Credential) (kimiCodeLogin, error) {
	var login kimiCodeLogin
	if c.Schema != kimiCodeSchema || len(c.Payload) == 0 || len(c.Payload) > 2*kimiCodeMaxFileBytes {
		return login, ErrSchema
	}
	var files map[string]string
	if json.Unmarshal(c.Payload, &files) != nil || len(files) != 2 {
		return login, ErrSchema
	}
	login.config = json.RawMessage(files["config.toml"])
	provider, err := parseKimiCodeConfig(login.config)
	if err != nil {
		return login, err
	}
	login.tokenPath, err = kimiCodeTokenFile(provider.OAuth.Key)
	if err != nil {
		return login, err
	}
	login.token = json.RawMessage(files[login.tokenPath])
	token, err := parseKimiCodeToken(login.token)
	if err != nil {
		return login, err
	}
	login.baseURL = provider.BaseURL
	login.oauthHost = provider.OAuth.OAuthHost
	login.tokenInfo = token
	login.expiresAt = time.Unix(token.ExpiresAt, 0).UTC()
	return login, nil
}

func capturedKimiCode(login kimiCodeLogin) (Captured, error) {
	credential, err := packKimiCodeLogin(login)
	if err != nil {
		return Captured{}, err
	}
	label := "Kimi Code"
	switch strings.TrimSuffix(login.baseURL, "/") {
	case "https://api.kimi.com/coding/v1":
		label = "Kimi.com"
	case "https://api.kimi.ai/coding/v1":
		label = "Kimi.ai"
	}
	return Captured{Identity: kimiCodeIdentity, Label: label, ExpiresAt: login.expiresAt, Credential: credential}, nil
}

func (m *kimiCodeManager) capture(root string) ([]Captured, error) {
	login, err := readKimiCodeLogin(root)
	if err != nil {
		return nil, err
	}
	captured, err := capturedKimiCode(login)
	if err != nil {
		return nil, err
	}
	return []Captured{captured}, nil
}

func (m *kimiCodeManager) Capture(ctx context.Context, root string) ([]Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := m.root(root)
	if err != nil {
		return nil, err
	}
	return m.capture(root)
}

func kimiCodeLock(ctx context.Context, root string) (*os.File, error) {
	path, err := kimiCodePath(root, ".dyna-account.lock")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("Kimi Code credentials could not be locked.")
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, errors.New("Kimi Code credentials could not be locked.")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *kimiCodeManager) Activate(ctx context.Context, integration string, c store.Credential) ([]Captured, error) {
	return m.activate(ctx, integration, c, nil)
}

func (m *kimiCodeManager) activate(ctx context.Context, integration string, c store.Credential, refresh *activeRefresh) (previous []Captured, err error) {
	if integration != "" {
		return nil, ErrSchema
	}
	login, err := unpackKimiCodeLogin(c)
	if err != nil {
		return nil, err
	}
	root, err := m.root("")
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, errors.New("Kimi Code account directory could not be created.")
	}
	if _, err = kimiCodePath(root, "config.toml"); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Join(root, "credentials"), 0700); err != nil {
		return nil, errors.New("Kimi Code credential directory could not be created.")
	}
	lock, err := kimiCodeLock(ctx, root)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	previous, err = m.capture(root)
	if err != nil && !errors.Is(err, ErrNotSignedIn) {
		return previous, err
	}
	if refresh != nil {
		if len(previous) != 1 || previous[0].Identity != refresh.current.Identity || previous[0].Integration != refresh.current.Integration {
			return previous, ErrNoIdentity
		}
		source := previous[0]
		if NewerCredential(refresh.current, source) {
			source = refresh.current
		}
		next, e := m.Refresh(ctx, source)
		refresh.result = next
		if e != nil {
			return previous, e
		}
		if e = refresh.checkpoint(next); e != nil {
			return previous, e
		}
		login, e = unpackKimiCodeLogin(next.Credential)
		if e != nil {
			return previous, e
		}
	}
	if err = ctx.Err(); err != nil {
		return previous, err
	}
	configPath, err := kimiCodePath(root, "config.toml")
	if err != nil {
		return previous, err
	}
	tokenPath, err := kimiCodePath(root, login.tokenPath)
	if err != nil {
		return previous, err
	}
	if err = fsutil.WriteFile(configPath, login.config, 0600, true); err != nil {
		return previous, errors.New("Kimi Code configuration could not be written.")
	}
	if err = fsutil.WriteFile(tokenPath, login.token, 0600, true); err != nil {
		return previous, ErrPartialActivation
	}
	if err = fsutil.SyncDir(filepath.Dir(tokenPath)); err != nil {
		return previous, ErrPartialActivation
	}
	if err = fsutil.SyncDir(root); err != nil {
		return previous, ErrPartialActivation
	}
	return previous, nil
}

func (*kimiCodeManager) CredentialExpiry(c Captured) (time.Time, error) {
	return credentialExpiry(c)
}

func (m *kimiCodeManager) LoginCommand(root, _ string) ([]string, []string, error) {
	home, err := m.root(root)
	if err != nil {
		return nil, nil, err
	}
	executable := m.host.Executable
	if executable == "" {
		executable = "kimi"
	}
	return []string{executable, "login"}, []string{
		"KIMI_CODE_HOME=" + home,
		"KIMI_CODE_NO_AUTO_UPDATE=1",
		"KIMI_DISABLE_TELEMETRY=1",
	}, nil
}

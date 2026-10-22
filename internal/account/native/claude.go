package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/account/store"
)

const claudeSchema = "claude-code/oauth-file@1"
const claudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
const claudeTokenURL = "https://platform.claude.com/v1/oauth/token"
const claudeUserAgent = "dyna-account-manager/1.0"
const claudeMaxJSON = 2 << 20

var claudeUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// These names are cleared only in the login child, never in the owner's shell.
var claudeAuthEnv = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR",
	"CLAUDE_CODE_HOST_CREDS_FILE", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
	"ANTHROPIC_PROFILE", "ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID",
	"ANTHROPIC_BASE_URL", "ANTHROPIC_UNIX_SOCKET", "CLAUDE_CODE_REMOTE_SESSION_ID",
}

type claudeHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}
type claudeManager struct {
	host     Host
	client   claudeHTTPClient
	tokenURL string
	usageURL string
	now      func() time.Time
	// Tests can simulate a failure after capture without changing real files.
	write func(string, map[string]json.RawMessage) error
}
type claudePayload struct {
	Credentials  map[string]json.RawMessage `json:"credentials"`
	OAuthAccount json.RawMessage            `json:"oauthAccount"`
}
type claudeTokens struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    int64    `json:"expiresAt"`
	Scopes       []string `json:"scopes"`
	ClientID     string   `json:"clientId"`
}
type claudePaths struct{ dir, secretDir, credentials, config string }

func init() {
	register("claude-code", func(h Host) Manager {
		return &claudeManager{host: h, client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, tokenURL: claudeTokenURL, now: time.Now}
	})
}

// ClaudeObservation contains cached native identity and plan only. It is not
// remote identity verification and never includes a credential payload.
type ClaudeObservation struct {
	Identity string
	Email    string
	Label    string
	Plan     string
}

func (m *claudeManager) Harness() string { return "claude-code" }
func (m *claudeManager) Caps() Caps {
	supported := runtime.GOOS == "linux" && m.environmentSupported() && !m.profileConfigured()
	if supported {
		paths, err := m.paths("")
		if err != nil {
			supported = false
		} else if _, _, err = m.read(paths); err != nil {
			supported = false
		}
	}
	return Caps{Activate: supported, IsolatedLogin: runtime.GOOS == "linux", Refresh: supported,
		Scope:         "All Claude Code processes using this native credential and configuration directory.",
		RunningEffect: "This changes the shared Claude Code sign-in. Running sessions may use the new account on later requests; restart them for consistent account selection."}
}
func claudeTruthy(s string) bool { return s != "" && s != "0" && !strings.EqualFold(s, "false") }
func claudeEnvConflict(key, value string) bool {
	switch key {
	case "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST":
		return claudeTruthy(value)
	default:
		return value != ""
	}
}
func (m *claudeManager) environmentSupported() bool {
	for _, key := range claudeAuthEnv {
		if key == "ANTHROPIC_BASE_URL" {
			if v := m.host.Getenv(key); v != "" && strings.TrimRight(v, "/") != "https://api.anthropic.com" {
				return false
			}
			continue
		}
		if claudeEnvConflict(key, m.host.Getenv(key)) {
			return false
		}
	}
	return true
}
func (m *claudeManager) path(value string) (string, error) {
	if value == "~" {
		value = m.host.HomeDir()
	} else if strings.HasPrefix(value, "~/") {
		value = filepath.Join(m.host.HomeDir(), value[2:])
	}
	if value == "" || !filepath.IsAbs(value) {
		return "", ErrUnsupported
	}
	return filepath.Clean(value), nil
}
func (m *claudeManager) paths(root string) (claudePaths, error) {
	var p claudePaths
	var err error
	if root != "" {
		p.dir, err = m.path(root)
		p.secretDir = p.dir
		if err != nil {
			return p, err
		}
		p.config = filepath.Join(p.dir, ".claude.json")
	} else {
		v := m.host.Getenv("CLAUDE_CONFIG_DIR")
		if v == "" {
			v = filepath.Join(m.host.HomeDir(), ".claude")
		}
		p.dir, err = m.path(v)
		if err != nil {
			return p, err
		}
		p.secretDir = p.dir
		if v = m.host.Getenv("CLAUDE_SECURESTORAGE_CONFIG_DIR"); v != "" {
			p.secretDir, err = m.path(v)
			if err != nil {
				return p, err
			}
		}
		configRoot := m.host.Getenv("CLAUDE_CONFIG_DIR")
		if configRoot == "" {
			configRoot = m.host.HomeDir()
		}
		configRoot, err = m.path(configRoot)
		if err != nil {
			return p, err
		}
		p.config = filepath.Join(configRoot, ".claude.json")
	}
	// The shipped program selects a populated legacy config before .claude.json.
	// TODO: legacy/shim remove later — reads the old Claude .config.json location; remove after supported Claude versions and stored configs use .claude.json
	legacy := filepath.Join(p.dir, ".config.json")
	if st, e := os.Lstat(legacy); e == nil {
		if !st.Mode().IsRegular() {
			return p, ErrUnsupported
		}
		_, modernErr := os.Lstat(p.config)
		if st.Size() > 0 || st.Mode().Perm()&0222 != 0 && os.IsNotExist(modernErr) {
			p.config = legacy
		}
	} else if !os.IsNotExist(e) {
		return p, claudeFileError()
	}
	p.credentials = filepath.Join(p.secretDir, ".credentials.json")
	return p, nil
}
func claudeFileError() error {
	return errors.New("Claude Code sign-in files could not be read or written safely.")
}

// Bound reads refuse symlinks, special files, and oversized data. Errors never
// retain a path, raw native JSON, or an underlying message that could hold auth.
func claudeReadBytes(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil, nil
		}
		return nil, claudeFileError()
	}
	f := os.NewFile(uintptr(fd), "claude-auth")
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > claudeMaxJSON {
		return nil, claudeFileError()
	}
	b, err := io.ReadAll(io.LimitReader(f, claudeMaxJSON+1))
	if err != nil || len(b) > claudeMaxJSON {
		return nil, claudeFileError()
	}
	return b, nil
}
func claudeReadObject(path string) (map[string]json.RawMessage, error) {
	b, err := claudeReadBytes(path)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return map[string]json.RawMessage{}, nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(b, &obj) != nil || obj == nil {
		return nil, ErrSchema
	}
	return obj, nil
}
func (m *claudeManager) profileConfigured() bool {
	root := m.host.Getenv("ANTHROPIC_CONFIG_DIR")
	if root == "" {
		root = m.host.Getenv("XDG_CONFIG_HOME")
		if root != "" {
			root = filepath.Join(root, "anthropic")
		} else {
			root = filepath.Join(m.host.HomeDir(), ".config", "anthropic")
		}
	}
	root, err := m.path(root)
	if err != nil {
		return true
	}
	name := "default"
	if b, e := claudeReadBytes(filepath.Join(root, "active_config")); e == nil && b != nil {
		name = strings.TrimSpace(string(b))
	} else if e != nil {
		return true
	}
	if name == "" {
		name = "default"
	}
	if filepath.Base(name) != name || name == "." || name == ".." {
		return true
	}
	obj, err := claudeReadObject(filepath.Join(root, "configs", name+".json"))
	if err != nil {
		return true
	}
	return len(obj) > 0
}
func claudeNativeSupported(creds, config, settings map[string]json.RawMessage) bool {
	for _, key := range []string{"enterpriseGateway"} {
		if raw := creds[key]; len(raw) > 0 && string(raw) != "null" {
			return false
		}
	}
	for _, key := range []string{"primaryApiKey", "apiKeyHelper"} {
		var value string
		if raw := config[key]; len(raw) > 0 && string(raw) != "null" {
			if json.Unmarshal(raw, &value) != nil || value != "" {
				return false
			}
		}
	}
	if raw := settings["apiKeyHelper"]; len(raw) > 0 && string(raw) != "null" && string(raw) != `""` {
		return false
	}
	var forced string
	_ = json.Unmarshal(settings["forceLoginMethod"], &forced)
	if forced != "" && forced != "claudeai" {
		return false
	}
	var env map[string]string
	if raw := settings["env"]; len(raw) > 0 && json.Unmarshal(raw, &env) != nil {
		return false
	}
	for _, key := range claudeAuthEnv {
		if key == "ANTHROPIC_BASE_URL" {
			if v := env[key]; v != "" && strings.TrimRight(v, "/") != "https://api.anthropic.com" {
				return false
			}
			continue
		}
		if claudeEnvConflict(key, env[key]) {
			return false
		}
	}
	return true
}
func claudeDecode(c store.Credential) (claudePayload, claudeTokens, string, string, error) {
	var p claudePayload
	var tok claudeTokens
	if c.Schema != claudeSchema || len(c.Payload) > claudeMaxJSON || json.Unmarshal(c.Payload, &p) != nil || len(p.Credentials) != 1 {
		return p, tok, "", "", ErrSchema
	}
	if json.Unmarshal(p.Credentials["claudeAiOauth"], &tok) != nil || tok.AccessToken == "" || tok.RefreshToken == "" || tok.ExpiresAt <= 0 {
		return p, tok, "", "", ErrSchema
	}
	inference := false
	for _, scope := range tok.Scopes {
		if scope == "user:inference" {
			inference = true
		}
	}
	if !inference {
		return p, tok, "", "", ErrSchema
	}
	var account struct {
		UUID  string `json:"accountUuid"`
		Email string `json:"emailAddress"`
	}
	if json.Unmarshal(p.OAuthAccount, &account) != nil || !claudeUUID.MatchString(account.UUID) {
		return p, tok, "", "", ErrNoIdentity
	}
	return p, tok, "anthropic:" + strings.ToLower(account.UUID), account.Email, nil
}
func claudeCaptured(creds, config map[string]json.RawMessage) ([]Captured, error) {
	raw := creds["claudeAiOauth"]
	if len(raw) == 0 || string(raw) == "null" {
		return nil, ErrNotSignedIn
	}
	var tok claudeTokens
	if json.Unmarshal(raw, &tok) != nil {
		return nil, ErrSchema
	}
	if tok.AccessToken == "" && tok.RefreshToken == "" {
		return nil, ErrNotSignedIn
	}
	p := claudePayload{Credentials: map[string]json.RawMessage{"claudeAiOauth": raw}, OAuthAccount: config["oauthAccount"]}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, ErrSchema
	}
	c := store.Credential{Schema: claudeSchema, Payload: data}
	_, tok, id, label, err := claudeDecode(c)
	if err != nil {
		return nil, err
	}
	if label == "" {
		label = "Claude account"
	}
	return []Captured{{Identity: id, Label: label, Credential: c, ExpiresAt: time.UnixMilli(tok.ExpiresAt)}}, nil
}
func (m *claudeManager) read(p claudePaths) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	creds, err := claudeReadObject(p.credentials)
	if err != nil {
		return nil, nil, err
	}
	config, err := claudeReadObject(p.config)
	if err != nil {
		return nil, nil, err
	}
	settings, err := claudeReadObject(filepath.Join(p.dir, "settings.json"))
	if err != nil {
		return nil, nil, err
	}
	if !claudeNativeSupported(creds, config, settings) {
		return nil, nil, ErrUnsupported
	}
	return creds, config, nil
}
func (m *claudeManager) Capture(ctx context.Context, root string) ([]Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" || root == "" && (!m.environmentSupported() || m.profileConfigured()) {
		return nil, ErrUnsupported
	}
	p, err := m.paths(root)
	if err != nil {
		return nil, err
	}
	creds, config, err := m.read(p)
	if err != nil {
		return nil, err
	}
	return claudeCaptured(creds, config)
}

// proper-lockfile uses atomic mkdir, mtime heartbeats, and rmdir. We never
// steal an existing lock: a stale native holder must be resolved by Claude.
// Inode checks prevent releasing a replacement lock after compromise.
type claudeDirLock struct {
	path   string
	info   os.FileInfo
	mu     sync.Mutex
	broken bool
	stop   chan struct{}
	done   chan struct{}
}

func claudeLock(ctx context.Context, path string) (*claudeDirLock, error) {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := os.Mkdir(path, 0700)
		if err == nil {
			info, e := os.Lstat(path)
			if e != nil {
				return nil, claudeFileError()
			}
			l := &claudeDirLock{path: path, info: info, stop: make(chan struct{}), done: make(chan struct{})}
			go func() {
				defer close(l.done)
				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-l.stop:
						return
					case <-ticker.C:
						l.mu.Lock()
						st, e := os.Lstat(l.path)
						if e != nil || !os.SameFile(l.info, st) {
							l.broken = true
						} else {
							now := time.Now()
							if os.Chtimes(l.path, now, now) != nil {
								l.broken = true
							}
						}
						l.mu.Unlock()
					}
				}
			}()
			return l, nil
		}
		if !os.IsExist(err) {
			return nil, claudeFileError()
		}
		wait := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			wait.Stop()
			return nil, ctx.Err()
		case <-deadline.C:
			wait.Stop()
			return nil, errors.New("Claude Code is busy updating its sign-in. Try again.")
		case <-wait.C:
		}
	}
}
func (l *claudeDirLock) check() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, err := os.Lstat(l.path)
	if l.broken || err != nil || !os.SameFile(l.info, st) {
		return errors.New("Claude Code sign-in lock changed. Try again.")
	}
	return nil
}
func (l *claudeDirLock) close() {
	close(l.stop)
	<-l.done
	l.mu.Lock()
	defer l.mu.Unlock()
	if st, err := os.Lstat(l.path); err == nil && os.SameFile(l.info, st) {
		_ = os.Remove(l.path)
	}
}
func claudeAtomicJSON(path string, obj map[string]json.RawMessage) error {
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return claudeFileError()
	} else if err != nil && !os.IsNotExist(err) {
		return claudeFileError()
	}
	b, err := json.MarshalIndent(obj, "", "  ")
	if err != nil || len(b) > claudeMaxJSON {
		return ErrSchema
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dyna-claude-*")
	if err != nil {
		return claudeFileError()
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return claudeFileError()
	}
	if err = os.Rename(name, path); err != nil {
		return claudeFileError()
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return ErrPartialActivation
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrPartialActivation
	}
	return nil
}
func claudeJSONEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	left, _ := json.Marshal(x)
	right, _ := json.Marshal(y)
	return bytes.Equal(left, right)
}
func (m *claudeManager) Activate(ctx context.Context, integration string, c store.Credential) ([]Captured, error) {
	return m.activate(ctx, integration, c, nil)
}
func (m *claudeManager) activate(ctx context.Context, integration string, c store.Credential, refresh *activeRefresh) (previous []Captured, err error) {
	if integration != "" || runtime.GOOS != "linux" || !m.environmentSupported() || m.profileConfigured() {
		return nil, ErrUnsupported
	}
	payload, _, _, _, err := claudeDecode(c)
	if err != nil {
		return nil, err
	}
	p, err := m.paths("")
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{p.dir, p.secretDir, filepath.Dir(p.config)} {
		if os.MkdirAll(dir, 0700) != nil {
			return nil, claudeFileError()
		}
	}
	canonical, err := filepath.EvalSymlinks(p.secretDir)
	if err != nil {
		return nil, claudeFileError()
	}
	paths := []string{filepath.Join(p.secretDir, ".oauth_refresh.lock"), canonical + ".lock", p.config + ".lock", filepath.Join(p.secretDir, ".storage-write.lock")}
	locks := make([]*claudeDirLock, 0, len(paths))
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].close()
		}
	}()
	for _, path := range paths {
		lock, e := claudeLock(ctx, path)
		if e != nil {
			return nil, e
		}
		locks = append(locks, lock)
	}
	creds, config, err := m.read(p)
	if err != nil {
		return nil, err
	}
	previous, err = claudeCaptured(creds, config)
	if err != nil && !errors.Is(err, ErrNotSignedIn) {
		return nil, err
	}
	err = nil
	if refresh != nil && refresh.onlyNewer {
		// A login of another user is an old sign-in in this slot; replace it.
		if len(previous) == 1 && previous[0].Identity == refresh.current.Identity && !NewerCredential(refresh.current, previous[0]) {
			return previous, errNotNewer
		}
	} else if refresh != nil {
		if len(previous) != 1 || previous[0].Identity != refresh.current.Identity || previous[0].Integration != refresh.current.Integration {
			return previous, ErrNoIdentity
		}
		source := previous[0]
		if NewerCredential(refresh.current, source) {
			source = refresh.current
		}
		next, e := m.refresh(ctx, source, false)
		if e != nil {
			return previous, e
		}
		refresh.result = next
		if e = refresh.checkpoint(next); e != nil {
			return previous, e
		}
		payload, _, _, _, e = claudeDecode(next.Credential)
		if e != nil {
			return previous, e
		}
	}
	oldCreds := make(map[string]json.RawMessage, len(creds))
	for k, v := range creds {
		oldCreds[k] = v
	}
	oldConfig := make(map[string]json.RawMessage, len(config))
	for k, v := range config {
		oldConfig[k] = v
	}
	creds["claudeAiOauth"] = payload.Credentials["claudeAiOauth"]
	config["oauthAccount"] = payload.OAuthAccount
	write := m.write
	if write == nil {
		write = claudeAtomicJSON
	}
	for _, lock := range locks {
		if err = lock.check(); err != nil {
			return previous, err
		}
	}
	if err = ctx.Err(); err != nil {
		return previous, err
	}
	if err = write(p.credentials, creds); err != nil {
		return previous, err
	}
	for _, lock := range locks {
		if lock.check() != nil {
			return previous, ErrPartialActivation
		}
	}
	if err = write(p.config, config); err != nil {
		// Best-effort repair remains inside the native lock span. The caller must
		// still handle partial activation because native state has been written.
		for _, lock := range locks {
			if lock.check() != nil {
				return previous, ErrPartialActivation
			}
		}
		_ = write(p.credentials, oldCreds)
		_ = write(p.config, oldConfig)
		return previous, ErrPartialActivation
	}
	for _, lock := range locks {
		if lock.check() != nil {
			return previous, ErrPartialActivation
		}
	}
	actualCreds, actualConfig, err := m.read(p)
	if err != nil {
		return previous, ErrPartialActivation
	}
	if !claudeJSONEqual(actualCreds["claudeAiOauth"], payload.Credentials["claudeAiOauth"]) || !claudeJSONEqual(actualConfig["oauthAccount"], payload.OAuthAccount) {
		return previous, ErrPartialActivation
	}
	got, err := claudeCaptured(actualCreds, actualConfig)
	if err != nil {
		return previous, ErrPartialActivation
	}
	_, _, want, _, _ := claudeDecode(c)
	if len(got) != 1 || got[0].Identity != want {
		return previous, ErrPartialActivation
	}
	return previous, nil
}

// ActivateNewer writes c only when no login is in place or c expires later
// than it. It decides under the same native locks as Claude Code's own
// refresh, so a token that a running turn just rotated is never replaced by
// an older copy.
func (m *claudeManager) ActivateNewer(ctx context.Context, c Captured) (bool, error) {
	_, err := m.activate(ctx, c.Integration, c.Credential, &activeRefresh{current: c, onlyNewer: true})
	if errors.Is(err, errNotNewer) {
		return false, nil
	}
	return err == nil, err
}

// WriteOAuthAccount writes only the oauthAccount of a login into the Claude
// config, under the config lock that Claude Code uses. Credentials stay as
// they are, so a switch can change the account metadata while each turn keeps
// its own credentials folder.
func (m *claudeManager) WriteOAuthAccount(ctx context.Context, c store.Credential) error {
	payload, _, _, _, err := claudeDecode(c)
	if err != nil {
		return err
	}
	p, err := m.paths("")
	if err != nil {
		return err
	}
	if os.MkdirAll(filepath.Dir(p.config), 0700) != nil {
		return claudeFileError()
	}
	lock, err := claudeLock(ctx, p.config+".lock")
	if err != nil {
		return err
	}
	defer lock.close()
	config, err := claudeReadObject(p.config)
	if err != nil {
		return err
	}
	config["oauthAccount"] = payload.OAuthAccount
	write := m.write
	if write == nil {
		write = claudeAtomicJSON
	}
	if err = lock.check(); err != nil {
		return err
	}
	return write(p.config, config)
}

func (m *claudeManager) Refresh(ctx context.Context, c Captured) (Captured, error) {
	return m.refresh(ctx, c, true)
}
func (m *claudeManager) refresh(ctx context.Context, c Captured, inactive bool) (Captured, error) {
	if err := ctx.Err(); err != nil {
		return c, err
	}
	p, tok, id, _, err := claudeDecode(c.Credential)
	if err != nil {
		return c, err
	}
	if c.Identity != id || c.Integration != "" {
		return c, ErrNoIdentity
	}
	now := time.Now()
	if m.now != nil {
		now = m.now()
	}
	if time.UnixMilli(tok.ExpiresAt).After(now.Add(5 * time.Minute)) {
		return c, nil
	}
	if runtime.GOOS != "linux" || !m.environmentSupported() || m.profileConfigured() {
		return c, ErrUnsupported
	}
	if inactive {
		active, e := m.Capture(ctx, "")
		if e != nil && !errors.Is(e, ErrNotSignedIn) {
			return c, e
		}
		for _, a := range active {
			if a.Identity == c.Identity {
				return c, ErrUnsupported
			}
		}
	}
	clientID := tok.ClientID
	if clientID == "" {
		clientID = claudeClientID
	}
	body, _ := json.Marshal(map[string]any{"grant_type": "refresh_token", "refresh_token": tok.RefreshToken, "client_id": clientID, "scope": strings.Join(tok.Scopes, " ")})
	endpoint := m.tokenURL
	if endpoint == "" {
		endpoint = claudeTokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return c, errors.New("Claude Code sign-in could not be refreshed.")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", claudeUserAgent)
	client := m.client
	if client == nil {
		return c, ErrUnsupported
	}
	resp, err := client.Do(req)
	if err != nil {
		return c, errors.New("Claude Code sign-in could not be refreshed.")
	}
	defer resp.Body.Close()
	if err := directStatusError(resp, now); err != nil {
		return c, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, claudeMaxJSON+1))
	if err != nil || len(raw) > claudeMaxJSON {
		return c, errors.New("Claude Code sign-in could not be refreshed.")
	}
	var out struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int64  `json:"expires_in"`
		RefreshExpiresIn int64  `json:"refresh_token_expires_in"`
		Scope            string `json:"scope"`
		Error            string `json:"error"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return c, errors.New("Claude Code sign-in could not be refreshed.")
	}
	if out.Error == "invalid_grant" || resp.StatusCode == http.StatusUnauthorized {
		return c, ErrInvalidGrant
	}
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" || out.ExpiresIn <= 0 || out.ExpiresIn > 10*365*86400 {
		return c, errors.New("Claude Code sign-in could not be refreshed.")
	}
	var native map[string]json.RawMessage
	_ = json.Unmarshal(p.Credentials["claudeAiOauth"], &native)
	set := func(k string, v any) { native[k], _ = json.Marshal(v) }
	set("accessToken", out.AccessToken)
	if out.RefreshToken != "" {
		set("refreshToken", out.RefreshToken)
	}
	expiry := now.Add(time.Duration(out.ExpiresIn) * time.Second)
	set("expiresAt", expiry.UnixMilli())
	if out.RefreshExpiresIn > 0 && out.RefreshExpiresIn <= 10*365*86400 {
		set("refreshTokenExpiresAt", now.Add(time.Duration(out.RefreshExpiresIn)*time.Second).UnixMilli())
	}
	if out.Scope != "" {
		set("scopes", strings.Fields(out.Scope))
	}
	p.Credentials["claudeAiOauth"], _ = json.Marshal(native)
	c.Credential.Payload, _ = json.Marshal(p)
	c.ExpiresAt = expiry
	if _, _, _, _, err := claudeDecode(c.Credential); err != nil {
		return c, err
	}
	return c, nil
}
func (m *claudeManager) LoginCommand(root, integration string) ([]string, []string, error) {
	executable := m.host.Executable
	if executable == "" {
		executable = "claude"
	}
	argv := []string{executable, "auth", "login", "--claudeai"}
	env := make([]string, 0, len(claudeAuthEnv)+3)
	for _, key := range claudeAuthEnv {
		env = append(env, key+"=")
	}
	if root != "" {
		if runtime.GOOS != "linux" {
			return nil, nil, ErrUnsupported
		}
		dir, err := m.path(root)
		if err != nil {
			return nil, nil, err
		}
		env = append(env, "CLAUDE_CONFIG_DIR="+dir, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+dir, "ANTHROPIC_CONFIG_DIR="+filepath.Join(dir, "anthropic"))
	}
	return argv, env, nil
}

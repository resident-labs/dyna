package native

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/boundedio"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/fsutil"
)

// TODO: legacy/shim remove later — retain saved Secret Service payloads until all saved accounts use a new schema.
const agyAuthSchema = "antigravity/secret-service@1"
const agyRole = "keyring:gemini/antigravity"
const agyMaxAuth = 4 << 20

// AntigravityAuthFile is relative to the native HOME.
const AntigravityAuthFile = ".gemini/antigravity-cli/antigravity-oauth-token"

// AntigravityFileEnv selects agy 1.4.2's native SSH file-store branch.
func AntigravityFileEnv() map[string]string {
	return map[string]string{
		"SSH_TTY":            "dyna-private-file-store",
		"JETSKI_OAUTH_TOKEN": "", "GEMINI_API_KEY": "", "AGY_ADC_AUTH": "",
		"AGY_LLM_GATEWAY_API_KEY": "", "ANTIGRAVITY_GEMINI_DIR": "", "JETSKI_APP_DATA_DIR": "",
	}
}

var agyNativeMu sync.Mutex

// Tests replace the token store to inject read and write failures.
type agyTokenStore interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
}
type antigravityManager struct {
	host          Host
	storeOverride agyTokenStore
	legacy        agySecretReader
	client        claudeHTTPClient
	now           func() time.Time
	clientSecret  func(context.Context) (string, error)
}
type agySecretReader interface {
	Read(context.Context) ([]byte, error)
}

// TODO: legacy/shim remove later — read old shared grants until existing Dyna homes have private token files.
type agySecretTool struct {
	path string
	host Host
}

func init()                                 { register("antigravity", func(h Host) Manager { return &antigravityManager{host: h} }) }
func (*antigravityManager) Harness() string { return "antigravity" }
func (*antigravityManager) Caps() Caps {
	return Caps{Activate: true, IsolatedLogin: true, Refresh: true, Scope: "Antigravity credentials in the selected home", RunningEffect: "New turns use the active native login after the harness gate drains."}
}

func (s agySecretTool) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	st, err := os.Stat(s.path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return nil, ErrUnsupported
	}
	cmd := exec.CommandContext(ctx, s.path, args...)
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"HOME=" + s.host.HomeDir(), "LANG=C.UTF-8"}
	for _, key := range []string{"DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR", "DISPLAY", "WAYLAND_DISPLAY"} {
		if v := s.host.Getenv(key); v != "" {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	return cmd, nil
}
func (s agySecretTool) Read(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd, err := s.command(ctx, "lookup", "service", "gemini", "username", "antigravity")
	if err != nil {
		return nil, err
	}
	out := boundedio.New(agyMaxAuth)
	stderr := boundedio.New(16 << 10)
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err = childenv.Clean(cmd).Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && out.Len() == 0 && stderr.Len() == 0 {
			return nil, ErrNotSignedIn
		}
		return nil, errors.New("Antigravity credentials could not be read from Secret Service.")
	}
	if out.Truncated() {
		return nil, ErrSchema
	}
	raw := bytes.TrimSpace(out.Bytes())
	if len(raw) == 0 {
		return nil, ErrNotSignedIn
	}
	return append([]byte(nil), raw...), nil
}

type agyFileStore struct {
	path         string
	legacy       agySecretReader
	importLegacy bool
}

func (s agyFileStore) Read(ctx context.Context) ([]byte, error) {
	info, err := os.Lstat(s.path)
	if os.IsNotExist(err) {
		// TODO: legacy/shim remove later — import the old shared grant once, until existing Dyna homes have private token files.
		if s.legacy == nil {
			return nil, ErrNotSignedIn
		}
		raw, err := s.legacy.Read(ctx)
		if errors.Is(err, ErrUnsupported) {
			return nil, ErrNotSignedIn
		}
		if err != nil {
			return nil, err
		}
		if _, err = agyAccount(raw); err != nil {
			return nil, err
		}
		if s.importLegacy {
			if err = s.Write(ctx, raw); err != nil {
				return nil, err
			}
		}
		return raw, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("Antigravity credentials could not be read safely.")
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, errors.New("Antigravity credentials could not be read.")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, agyMaxAuth+1))
	if err != nil || len(raw) > agyMaxAuth {
		return nil, ErrSchema
	}
	return raw, nil
}
func (s agyFileStore) Write(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(raw) > agyMaxAuth || !json.Valid(raw) {
		return ErrSchema
	}
	if info, err := os.Lstat(s.path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("Antigravity credentials could not be written safely.")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	return fsutil.WriteFile(s.path, raw, 0600, true)
}
func (m *antigravityManager) store(root string) (agyTokenStore, error) {
	home := root
	if home == "" {
		home = m.host.HomeDir()
	}
	if !filepath.IsAbs(home) {
		return nil, ErrUnsupported
	}
	if root == "" {
		for key := range AntigravityFileEnv() {
			if key != "SSH_TTY" && m.host.Getenv(key) != "" {
				return nil, ErrUnsupported
			}
		}
		if m.storeOverride != nil {
			return m.storeOverride, nil
		}
	}
	var legacy agySecretReader
	if root == "" {
		legacy = m.legacy
		if legacy == nil {
			legacy = agySecretTool{path: "/usr/bin/secret-tool", host: m.host}
		}
	}
	return agyFileStore{
		path: filepath.Join(home, AntigravityAuthFile), legacy: legacy,
		importLegacy: root == "" && m.host.Getenv("SSH_TTY") == AntigravityFileEnv()["SSH_TTY"],
	}, nil
}
func agyAccount(raw []byte) (Captured, error) {
	if len(raw) > agyMaxAuth {
		return Captured{}, ErrSchema
	}
	var wrapper struct {
		Token *struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			Expiry       string `json:"expiry"`
		} `json:"token"`
		IDToken    string `json:"id_token"`
		AuthMethod string `json:"auth_method"`
		ProjectID  string `json:"project_id"`
		UserTier   string `json:"user_tier"`
	}
	if json.Unmarshal(raw, &wrapper) != nil {
		return Captured{}, ErrSchema
	}
	if wrapper.Token == nil || wrapper.Token.AccessToken == "" {
		return Captured{}, ErrNotSignedIn
	}
	if wrapper.IDToken == "" {
		return Captured{}, ErrNoIdentity
	}
	parts := strings.Split(wrapper.IDToken, ".")
	if len(wrapper.IDToken) > 64<<10 || len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return Captured{}, ErrNoIdentity
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Captured{}, ErrNoIdentity
	}
	var id struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if json.Unmarshal(claims, &id) != nil || id.Sub == "" || len(id.Sub) > 512 || strings.ContainsAny(id.Sub, "@\r\n\x00") {
		return Captured{}, ErrNoIdentity
	}
	if wrapper.AuthMethod != "consumer" && wrapper.AuthMethod != "gcp" {
		return Captured{}, ErrUnsupported
	}
	if wrapper.AuthMethod == "gcp" && (wrapper.ProjectID == "" || wrapper.UserTier == "") {
		return Captured{}, ErrSchema
	}
	var expiry time.Time
	if wrapper.Token.Expiry != "" {
		expiry, err = time.Parse(time.RFC3339Nano, wrapper.Token.Expiry)
		if err != nil {
			return Captured{}, ErrSchema
		}
	}
	return Captured{Identity: "google:" + id.Sub, Label: identityLabel(id.Email, wrapper.UserTier), Credential: accountstore.Credential{Schema: agyAuthSchema, Payload: agyPack(raw, nil)}, ExpiresAt: expiry}, nil
}
func agyCapture(ctx context.Context, store agyTokenStore, acp agyFileStore) ([]Captured, error) {
	raw, err := store.Read(ctx)
	if err != nil {
		return nil, err
	}
	c, err := agyAccount(raw)
	if err != nil {
		return nil, err
	}
	if c, err = agyWithACP(ctx, c, acp); err != nil {
		return nil, err
	}
	return []Captured{c}, nil
}
func (m *antigravityManager) Capture(ctx context.Context, root string) ([]Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store, err := m.store(root)
	if err != nil {
		return nil, err
	}
	acp, err := m.acpStore(root)
	if err != nil {
		return nil, err
	}
	agyNativeMu.Lock()
	defer agyNativeMu.Unlock()
	return agyCapture(ctx, store, acp)
}
func (m *antigravityManager) Activate(ctx context.Context, integration string, c accountstore.Credential) ([]Captured, error) {
	return m.activate(ctx, integration, c, nil)
}
func (m *antigravityManager) activate(ctx context.Context, integration string, c accountstore.Credential, refresh *activeRefresh) ([]Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if integration != "" || c.Schema != agyAuthSchema || len(c.Payload) > agyMaxAuth {
		return nil, ErrSchema
	}
	raw, acpRaw, err := agyPayload(c.Payload)
	if err != nil {
		return nil, err
	}
	target, err := agyAccount(raw)
	if err != nil {
		return nil, err
	}
	store, err := m.store("")
	if err != nil {
		return nil, err
	}
	acp, err := m.acpStore("")
	if err != nil {
		return nil, err
	}
	agyNativeMu.Lock()
	defer agyNativeMu.Unlock()
	// The service holds Dyna's cross-process harness gate while switching files.
	previous, err := agyCapture(ctx, store, acp)
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
		if e != nil {
			return previous, e
		}
		refresh.result = next
		if e = refresh.checkpoint(next); e != nil {
			return previous, e
		}
		if raw, acpRaw, e = agyPayload(next.Credential.Payload); e != nil {
			return previous, e
		}
		target = next
	}
	// writeACP makes the home match a payload: an account without an ACP grant
	// must not inherit the previous account's.
	writeACP := func(ctx context.Context, grant json.RawMessage) error {
		if len(grant) == 0 {
			return acp.remove()
		}
		return acp.Write(ctx, grant)
	}
	rollback := func(cause error) ([]Captured, error) {
		if len(previous) > 0 {
			old, oldACP, e := agyPayload(previous[0].Credential.Payload)
			bg := context.WithoutCancel(ctx)
			if e != nil || store.Write(bg, old) != nil || writeACP(bg, oldACP) != nil {
				return previous, errors.Join(errors.New("Antigravity account update and rollback failed."), ErrPartialActivation)
			}
		}
		return previous, errors.Join(cause, ErrPartialActivation)
	}
	if err = store.Write(ctx, raw); err != nil {
		return rollback(err)
	}
	if err = writeACP(ctx, acpRaw); err != nil {
		return rollback(err)
	}
	after, err := agyCapture(ctx, store, acp)
	if err != nil {
		return rollback(err)
	}
	if len(after) != 1 || after[0].Identity != target.Identity {
		return rollback(ErrNoIdentity)
	}
	// Compare both files as well as identity, without exposing either.
	got, gotACP, err := agyPayload(after[0].Credential.Payload)
	if err != nil || !sameJSON(raw, got) || !sameJSON(acpRaw, gotACP) {
		return rollback(errors.New("Antigravity credentials could not be verified after writing."))
	}
	return previous, nil
}

// sameJSON compares two JSON documents by value; two absent documents match.
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	_ = json.Unmarshal(a, &x)
	_ = json.Unmarshal(b, &y)
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return bytes.Equal(ja, jb)
}
func (m *antigravityManager) LoginCommand(root, integration string) ([]string, []string, error) {
	if integration != "" || root != "" && !filepath.IsAbs(root) {
		return nil, nil, ErrUnsupported
	}
	exe := m.host.Executable
	if exe == "" {
		exe = "agy"
	}
	env := []string{}
	for key, value := range AntigravityFileEnv() {
		env = append(env, key+"="+value)
	}
	if root != "" {
		env = append(env, "HOME="+root)
	}
	return []string{exe}, env, nil
}

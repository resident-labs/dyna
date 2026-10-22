package native

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/fsutil"
)

// Antigravity accounts sign in through the ACP server only. A saved account is
// the ACP grant plus the Google identity that sign-in confirmed once.
const agyACPSchema = "antigravity/acp@1"

// TODO: legacy/shim remove later — read accounts saved before ACP-only sign-in (the agy CLI wrapper and an optional ACP grant) until they sign in again.
const agyAuthSchema = "antigravity/secret-service@1"
const agyRole = "keyring:gemini/antigravity"
const agyMaxAuth = 4 << 20

// AntigravityAccountFile records the Google identity and label of the ACP grant
// beside it, relative to the native HOME. Sign-in writes it once after Google
// confirms the account; capture and activation read and restore it offline.
const AntigravityAccountFile = "antigravity-acp/account.json"

// AntigravityFileEnv selects agy 1.4.2's native SSH file-store branch.
func AntigravityFileEnv() map[string]string {
	return map[string]string{
		"SSH_TTY":            "dyna-private-file-store",
		"JETSKI_OAUTH_TOKEN": "", "GEMINI_API_KEY": "", "AGY_ADC_AUTH": "",
		"AGY_LLM_GATEWAY_API_KEY": "", "ANTIGRAVITY_GEMINI_DIR": "", "JETSKI_APP_DATA_DIR": "",
	}
}

var agyNativeMu sync.Mutex

type antigravityManager struct {
	host         Host
	client       claudeHTTPClient
	now          func() time.Time
	clientSecret func(context.Context) (string, error)
}

func init()                                 { register("antigravity", func(h Host) Manager { return &antigravityManager{host: h} }) }
func (*antigravityManager) Harness() string { return "antigravity" }
// SupportsCredentialRefresh is true only for a legacy saved CLI token. The ACP
// server refreshes an ACP-only account's grant itself.
func (*antigravityManager) SupportsCredentialRefresh(c Captured) bool {
	return c.Credential.Schema == agyAuthSchema
}

func (*antigravityManager) Caps() Caps {
	return Caps{Activate: true, IsolatedLogin: true, Refresh: true, Scope: "Antigravity credentials in the selected home", RunningEffect: "New turns use the active native login after the harness gate drains."}
}

type agyFileStore struct{ path string }

func (s agyFileStore) Read(ctx context.Context) ([]byte, error) {
	info, err := os.Lstat(s.path)
	if os.IsNotExist(err) {
		return nil, ErrNotSignedIn
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

// agyACPAccount is the identity sign-in confirmed for an ACP grant.
type agyACPAccount struct {
	Identity string `json:"identity"`
	Label    string `json:"label,omitempty"`
}

func (a agyACPAccount) valid() bool {
	sub, ok := strings.CutPrefix(a.Identity, "google:")
	return ok && sub != "" && len(sub) <= 512 && !strings.ContainsAny(sub, "@\r\n\x00") && len(a.Label) <= 512
}

// agyACPCaptured is the saved form of an ACP-only account.
func agyACPCaptured(token []byte, a agyACPAccount) (Captured, error) {
	if !a.valid() || agyACPValidate(token) != nil {
		return Captured{}, ErrSchema
	}
	payload, err := json.Marshal(struct {
		agyACPAccount
		Token json.RawMessage `json:"token"`
	}{a, token})
	if err != nil {
		return Captured{}, ErrSchema
	}
	return Captured{Identity: a.Identity, Label: a.Label, Credential: accountstore.Credential{Schema: agyACPSchema, Payload: payload}}, nil
}

// agyACPUnpack returns the ACP grant and identity of a saved account.
func agyACPUnpack(c accountstore.Credential) (json.RawMessage, agyACPAccount, error) {
	if len(c.Payload) > agyMaxAuth {
		return nil, agyACPAccount{}, ErrSchema
	}
	switch c.Schema {
	case agyACPSchema:
		var p map[string]json.RawMessage
		var a agyACPAccount
		if json.Unmarshal(c.Payload, &p) != nil || json.Unmarshal(c.Payload, &a) != nil || !a.valid() {
			return nil, a, ErrSchema
		}
		for key := range p {
			if key != "identity" && key != "label" && key != "token" {
				return nil, a, ErrSchema
			}
		}
		if agyACPValidate(p["token"]) != nil {
			return nil, a, ErrSchema
		}
		return p["token"], a, nil
	case agyAuthSchema:
		// TODO: legacy/shim remove later — an account saved before ACP-only
		// sign-in names its identity in the saved agy CLI wrapper.
		cli, acp, err := agyPayload(c.Payload)
		if err != nil {
			return nil, agyACPAccount{}, err
		}
		legacy, err := agyAccount(cli)
		if err != nil {
			return nil, agyACPAccount{}, err
		}
		if len(acp) == 0 {
			return nil, agyACPAccount{}, errAgyNeedsACPSignIn
		}
		return acp, agyACPAccount{Identity: legacy.Identity, Label: legacy.Label}, nil
	}
	return nil, agyACPAccount{}, ErrSchema
}

var errAgyNeedsACPSignIn = errors.New("This Antigravity account was saved without an ACP sign-in. Sign in to it again.")

// agyHome holds the two files of one ACP sign-in in a native HOME.
type agyHome struct{ token, account agyFileStore }

func (m *antigravityManager) home(root string) (agyHome, error) {
	home := root
	if home == "" {
		home = m.host.HomeDir()
	}
	if !filepath.IsAbs(home) {
		return agyHome{}, ErrUnsupported
	}
	return agyHome{agyFileStore{filepath.Join(home, AntigravityACPTokenFile)}, agyFileStore{filepath.Join(home, AntigravityAccountFile)}}, nil
}

// capture reads the grant and the identity recorded at sign-in. It never asks
// Google. A grant without its recorded identity is not a Dyna sign-in.
func (h agyHome) capture(ctx context.Context) ([]Captured, error) {
	token, err := h.token.Read(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := h.account.Read(ctx)
	if err != nil {
		return nil, err
	}
	var a agyACPAccount
	if json.Unmarshal(raw, &a) != nil {
		return nil, ErrSchema
	}
	c, err := agyACPCaptured(token, a)
	if err != nil {
		return nil, err
	}
	return []Captured{c}, nil
}

// write makes the HOME hold exactly one account's grant and identity.
func (h agyHome) write(ctx context.Context, token json.RawMessage, a agyACPAccount) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return ErrSchema
	}
	if err = h.token.Write(ctx, token); err != nil {
		return err
	}
	return h.account.Write(ctx, raw)
}

// RecordAntigravityACPAccount records identity and label for the grant in home
// when the HOME predates recorded identities. It writes only when a grant
// exists and no record does, so it never renames a recorded sign-in.
//
// TODO: legacy/shim remove later — once every HOME signed in before ACP-only accounts has its record.
func RecordAntigravityACPAccount(home, identity, label string) error {
	a := agyACPAccount{Identity: identity, Label: label}
	if !filepath.IsAbs(home) || !a.valid() {
		return ErrUnsupported
	}
	agyNativeMu.Lock()
	defer agyNativeMu.Unlock()
	if _, err := (agyFileStore{filepath.Join(home, AntigravityACPTokenFile)}).Read(context.Background()); err != nil {
		if errors.Is(err, ErrNotSignedIn) {
			return nil
		}
		return err
	}
	raw, _ := json.Marshal(a)
	f, err := os.OpenFile(filepath.Join(home, AntigravityAccountFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (m *antigravityManager) Capture(ctx context.Context, root string) ([]Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h, err := m.home(root)
	if err != nil {
		return nil, err
	}
	agyNativeMu.Lock()
	defer agyNativeMu.Unlock()
	return h.capture(ctx)
}

// Activate writes one saved account's grant and identity into the HOME and
// verifies them. It makes no network call: the ACP server refreshes its own
// grant when it starts.
func (m *antigravityManager) Activate(ctx context.Context, integration string, c accountstore.Credential) ([]Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if integration != "" {
		return nil, ErrSchema
	}
	token, target, err := agyACPUnpack(c)
	if err != nil {
		return nil, err
	}
	h, err := m.home("")
	if err != nil {
		return nil, err
	}
	agyNativeMu.Lock()
	defer agyNativeMu.Unlock()
	// The service holds Dyna's cross-process harness gate while switching files.
	previous, err := h.capture(ctx)
	if err != nil && !errors.Is(err, ErrNotSignedIn) {
		return previous, err
	}
	rollback := func(cause error) ([]Captured, error) {
		bg := context.WithoutCancel(ctx)
		if len(previous) > 0 {
			oldToken, old, e := agyACPUnpack(previous[0].Credential)
			if e != nil || h.write(bg, oldToken, old) != nil {
				return previous, errors.Join(errors.New("Antigravity account update and rollback failed."), ErrPartialActivation)
			}
		} else if h.token.remove() != nil || h.account.remove() != nil {
			return previous, errors.Join(errors.New("Antigravity account update and rollback failed."), ErrPartialActivation)
		}
		return previous, errors.Join(cause, ErrPartialActivation)
	}
	if err = h.write(ctx, token, target); err != nil {
		return rollback(err)
	}
	after, err := h.capture(ctx)
	if err != nil {
		return rollback(err)
	}
	got, gotAccount, err := agyACPUnpack(after[0].Credential)
	if err != nil || gotAccount.Identity != target.Identity || !sameJSON(got, token) {
		return rollback(errors.New("Antigravity credentials could not be verified after writing."))
	}
	return previous, nil
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

package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"dyna-agent/internal/account/store"
	"dyna-agent/internal/childenv"
)

const openCodeRefSchema = "opencode/v2-ref@1"

// OpenCodeServiceLockFile protects private account commands during a restart.
const OpenCodeServiceLockFile = ".opencode-service.lock"

var openCodeRefID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]*$`)

type openCodeManager struct {
	host   Host
	client claudeHTTPClient
}

var _ NativeLister = (*openCodeManager)(nil)

type openCodeScope struct {
	Executable string `json:"executable"`
	Home       string `json:"home"`
	Data       string `json:"data"`
	Config     string `json:"config"`
	Cache      string `json:"cache"`
	State      string `json:"state"`
	Database   string `json:"database"`
}
type openCodeRef struct {
	CredentialType string        `json:"credentialType,omitempty"`
	Expires        int64         `json:"expires,omitempty"`
	Integration    string        `json:"integration"`
	CredentialID   string        `json:"credentialID"`
	Scope          openCodeScope `json:"scope"`
}
type openCodeRow struct {
	ID          string `json:"id"`
	Connections []struct {
		Type   string          `json:"type"`
		ID     string          `json:"id"`
		Name   string          `json:"name"`
		Status json.RawMessage `json:"status"`
	} `json:"connections"`
}

func init()                              { register("opencode", func(h Host) Manager { return &openCodeManager{host: h} }) }
func (*openCodeManager) Harness() string { return "opencode" }
func (*openCodeManager) Caps() Caps {
	return Caps{Activate: true, Scope: "OpenCode's shared native server, for the selected integration", RunningEffect: "OpenCode reloads the selected integration in its shared server. Later requests may use the new account; requests already sent keep their credentials."}
}

func (m *openCodeManager) scope() (openCodeScope, error) {
	s := openCodeScope{Home: m.host.HomeDir(), Executable: m.host.Executable}
	if !filepath.IsAbs(s.Home) {
		return s, ErrUnsupported
	}
	if s.Executable == "" {
		s.Executable = "opencode"
	}
	resolve := func(key, defaultRoot string) (string, error) {
		root := m.host.Getenv(key)
		if root == "" {
			root = filepath.Join(s.Home, defaultRoot)
		}
		if !filepath.IsAbs(root) {
			return "", ErrUnsupported
		}
		return filepath.Join(root, "opencode"), nil
	}
	var err error
	if s.Data, err = resolve("XDG_DATA_HOME", ".local/share"); err != nil {
		return s, err
	}
	if s.Config, err = resolve("XDG_CONFIG_HOME", ".config"); err != nil {
		return s, err
	}
	if s.Cache, err = resolve("XDG_CACHE_HOME", ".cache"); err != nil {
		return s, err
	}
	if s.State, err = resolve("XDG_STATE_HOME", ".local/state"); err != nil {
		return s, err
	}
	if config := m.host.Getenv("OPENCODE_CONFIG_DIR"); config != "" {
		if !filepath.IsAbs(config) {
			return s, ErrUnsupported
		}
		s.Config = filepath.Clean(config)
	}
	db := m.host.Getenv("OPENCODE_DB")
	if db == ":memory:" {
		return s, ErrUnsupported
	}
	if db == "" {
		db = "opencode-v2.db"
		if v := m.host.Getenv("OPENCODE_DISABLE_CHANNEL_DB"); v == "1" || v == "true" {
			db = "opencode.db"
		}
	}
	if !filepath.IsAbs(db) {
		db = filepath.Join(s.Data, db)
	}
	s.Database = filepath.Clean(db)
	return s, nil
}
func (m *openCodeManager) commandEnv(s openCodeScope) []string {
	// OpenCode 2.0.32 exits when OPENCODE_TEST_HOME is set but empty.
	overrides := map[string]string{"HOME": s.Home, "OPENCODE_TEST_HOME": s.Home, "XDG_DATA_HOME": filepath.Dir(s.Data), "XDG_CONFIG_HOME": filepath.Join(s.Home, ".config"), "XDG_CACHE_HOME": filepath.Dir(s.Cache), "XDG_STATE_HOME": filepath.Dir(s.State), "OPENCODE_CONFIG_DIR": s.Config, "OPENCODE_DB": s.Database}
	// Keep the native XDG config root even when CONFIG_DIR overrides its leaf.
	if v := m.host.Getenv("XDG_CONFIG_HOME"); v != "" {
		overrides["XDG_CONFIG_HOME"] = v
	}
	for _, key := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_PASSWORD", "OPENCODE_SERVER_PASSWORD", "OPENCODE_DISABLE_CHANNEL_DB"} {
		overrides[key] = m.host.Getenv(key)
	}
	env := os.Environ()
	// Overrides are appended last. exec.Cmd deduplicates environment keys.
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

type openCodeOutput struct{ bytes.Buffer }

func (b *openCodeOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, errors.New("OpenCode output exceeded its limit.")
	}
	return b.Buffer.Write(p)
}
func (m *openCodeManager) run(ctx context.Context, s openCodeScope, args ...string) ([]byte, error) {
	return m.runInput(ctx, s, nil, args...)
}

// runInput feeds stdin to the command, so a secret never appears in its arguments.
func (m *openCodeManager) runInput(ctx context.Context, s openCodeScope, stdin io.Reader, args ...string) ([]byte, error) {
	if m.host.OpenCodeService {
		unlock, err := lockOpenCode(ctx, filepath.Join(s.Config, OpenCodeServiceLockFile), syscall.LOCK_SH)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	// Waiting for a managed restart must not consume the command's budget.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Executable, args...)
	cmd.Env = m.commandEnv(s)
	cmd.Dir = s.Home
	cmd.Stderr = io.Discard
	cmd.Stdin = stdin
	cmd.WaitDelay = time.Second
	var out openCodeOutput
	cmd.Stdout = &out
	if err := childenv.Clean(cmd).Run(); err != nil {
		// A started native switch can commit before a later reload or transport
		// step fails. Let the caller restore the outgoing native reference.
		if cmd.Process != nil && len(args) >= 2 && args[0] == "auth" && args[1] == "switch" {
			return nil, ErrPartialActivation
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("OpenCode account command failed.")
	}
	return out.Bytes(), nil
}
func (m *openCodeManager) list(ctx context.Context, s openCodeScope) ([]openCodeRow, error) {
	args := []string{"auth", "list", "--format", "json"}
	// Personal discovery remains standalone. Dyna prepares a private managed
	// service so its concurrent turns and probes share one database writer.
	if !m.host.OpenCodeService {
		args = append(args, "--standalone")
	}
	raw, err := m.run(ctx, s, args...)
	if err != nil {
		return nil, err
	}
	var rows []openCodeRow
	if json.Unmarshal(raw, &rows) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, ErrSchema
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if !openCodeRefID.MatchString(row.ID) || seen[row.ID] {
			return nil, ErrSchema
		}
		seen[row.ID] = true
		ids := map[string]bool{}
		for _, c := range row.Connections {
			if c.Type == "credential" {
				if !openCodeRefID.MatchString(c.ID) || ids[c.ID] {
					return nil, ErrSchema
				}
				ids[c.ID] = true
			} else if c.Type != "env" || c.Name == "" {
				return nil, ErrSchema
			}
		}
	}
	return rows, nil
}
func openCodeAccountLabel(integration string) string {
	switch integration {
	case "opencode-go":
		return "OpenCode Go"
	case "opencode":
		return "OpenCode Zen"
	case "":
		return "OpenCode account"
	default:
		return "OpenCode · " + integration
	}
}

func openCodeCaptured(s openCodeScope, integration, id string) Captured {
	ref := openCodeRef{Integration: integration, CredentialID: id, Scope: s}
	payload, _ := json.Marshal(map[string]openCodeRef{"ref": ref})
	return Captured{Integration: integration, Identity: "opencode:" + integration + ":" + id, Label: openCodeAccountLabel(integration), Credential: store.Credential{Schema: openCodeRefSchema, Payload: payload}}
}
func (m *openCodeManager) captures(ctx context.Context, all bool) ([]Captured, error) {
	s, err := m.scope()
	if err != nil {
		return nil, err
	}
	rows, err := m.list(ctx, s)
	if err != nil {
		return nil, err
	}
	out := []Captured{}
	for _, row := range rows {
		if !store.IsOpenCodeIntegration(row.ID) {
			continue
		}
		credentials, _ := m.usageCredentials(ctx, s, row.ID)
		for index, c := range row.Connections {
			if c.Type == "credential" && (all || index == 0) {
				// A saved reference can survive a rejected grant. Only a usable
				// active credential proves that the native login is ready.
				if !all && len(c.Status) != 0 && !bytes.Equal(c.Status, []byte("null")) {
					continue
				}
				captured := openCodeCaptured(s, row.ID, c.ID)
				for _, credential := range credentials {
					if credential.ID != c.ID || credential.IntegrationID != row.ID || (credential.Value.Type == "key" && credential.Value.Key == "") {
						continue
					}
					ref, _ := openCodeUnpack(captured.Credential)
					ref.CredentialType = credential.Value.Type
					ref.Expires = credential.Value.Expires
					captured.Credential.Payload, _ = json.Marshal(map[string]openCodeRef{"ref": ref})
					if ref.Expires > 0 {
						captured.ExpiresAt = time.UnixMilli(ref.Expires).UTC()
					}
					break
				}
				out = append(out, captured)
			}
		}
	}
	return out, nil
}
func (m *openCodeManager) Capture(ctx context.Context, root string) ([]Captured, error) {
	if root != "" {
		return nil, ErrUnsupported
	}
	rows, err := m.captures(ctx, false)
	if err == nil && len(rows) == 0 {
		err = ErrNotSignedIn
	}
	return rows, err
}

// ListNative returns all saved native references for one-time import. OAuth
// grants remain native; this command can let OpenCode renew its own grants.
func (m *openCodeManager) ListNative(ctx context.Context) ([]Captured, error) {
	return m.captures(ctx, true)
}

func openCodeUnpack(c store.Credential) (openCodeRef, error) {
	var roles map[string]json.RawMessage
	var ref openCodeRef
	if c.Schema != openCodeRefSchema || len(c.Payload) > 64<<10 || json.Unmarshal(c.Payload, &roles) != nil || len(roles) != 1 {
		return ref, ErrSchema
	}
	// Reject unknown fields so this reference schema cannot carry copied grants.
	decoder := json.NewDecoder(bytes.NewReader(roles["ref"]))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ref) != nil || !openCodeRefID.MatchString(ref.Integration) || !openCodeRefID.MatchString(ref.CredentialID) {
		return ref, ErrSchema
	}
	return ref, nil
}
func openCodeSameScope(a, b openCodeScope) bool {
	return a.Home == b.Home && a.Data == b.Data && a.Config == b.Config && a.Database == b.Database
}

func (m *openCodeManager) Activate(ctx context.Context, integration string, c store.Credential) ([]Captured, error) {
	ref, err := openCodeUnpack(c)
	if err != nil {
		return nil, err
	}
	s, err := m.scope()
	if err != nil {
		return nil, err
	}
	// Executable paths change during package upgrades. Runtime cache/state paths
	// also do not identify the credential database. Bind refs to data scope only.
	if ref.Integration != integration || !openCodeSameScope(ref.Scope, s) {
		return nil, ErrSchema
	}
	if err = os.MkdirAll(s.State, 0700); err != nil {
		return nil, errors.New("OpenCode account lock directory could not be created.")
	}
	unlock, err := lockOpenCode(ctx, filepath.Join(s.State, ".dyna-account.lock"), syscall.LOCK_EX)
	if err != nil {
		return nil, err
	}
	defer unlock()

	rows, err := m.list(ctx, s)
	if err != nil {
		return nil, err
	}
	var previous []Captured
	found := false
	ready := false
	for _, row := range rows {
		if row.ID != integration {
			continue
		}
		for index, entry := range row.Connections {
			if entry.Type != "credential" {
				continue
			}
			if index == 0 {
				previous = append(previous, openCodeCaptured(s, row.ID, entry.ID))
			}
			if entry.ID == ref.CredentialID {
				found = true
				ready = len(entry.Status) == 0 || bytes.Equal(entry.Status, []byte("null"))
			}
		}
	}
	if !found {
		return previous, ErrNotSignedIn
	}
	if !ready {
		return previous, ErrInvalidGrant
	}
	_, err = m.run(ctx, s, "auth", "switch", integration, ref.CredentialID)
	return previous, err
}

// OpenCode 2.0.32 Console OAuth uses POST /auth/device/token with grant_type=refresh_token and client_id=opencode-cli.
func (*openCodeManager) Refresh(_ context.Context, c Captured) (Captured, error) { return c, nil }
func (m *openCodeManager) LoginCommand(root, integration string) ([]string, []string, error) {
	if root != "" {
		return nil, nil, ErrUnsupported
	}
	if integration != "" && !store.IsOpenCodeIntegration(integration) {
		return nil, nil, ErrSchema
	}
	s, err := m.scope()
	if err != nil {
		return nil, nil, err
	}
	// Return overrides, not a replacement environment. No files or commands here.
	env := []string{"HOME=" + s.Home, "XDG_DATA_HOME=" + filepath.Dir(s.Data), "XDG_CACHE_HOME=" + filepath.Dir(s.Cache), "XDG_STATE_HOME=" + filepath.Dir(s.State), "OPENCODE_CONFIG_DIR=" + s.Config, "OPENCODE_DB=" + s.Database}
	config := m.host.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(s.Home, ".config")
	}
	env = append(env, "XDG_CONFIG_HOME="+config)
	argv := []string{s.Executable, "auth", "login"}
	if integration != "" {
		argv = append(argv, integration)
	}
	return argv, env, nil
}

func lockOpenCode(ctx context.Context, path string, kind int) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("OpenCode account lock could not be opened.")
	}
	for {
		if err = ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), kind|syscall.LOCK_NB)
		if err == nil {
			return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, errors.New("OpenCode account lock failed.")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

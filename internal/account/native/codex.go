package native

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/account/store"
)

const codexAuthSchema = "codex/auth-file@1"
const codexMaxAuth = 4 << 20

type codexManager struct {
	host     Host
	client   *http.Client // Tests inject a local HTTP client and endpoint.
	tokenURL string
}

func init()                           { register("codex", func(h Host) Manager { return &codexManager{host: h} }) }
func (*codexManager) Harness() string { return "codex" }
func (*codexManager) Caps() Caps {
	return Caps{Activate: true, IsolatedLogin: true, Refresh: true, Scope: "Codex credentials for this host user", RunningEffect: "Running Codex sessions keep the old account and can write it back when they renew. Restart them after a switch."}
}

func (m *codexManager) root(root string) (string, error) {
	if root == "" {
		if m.host.Getenv("CODEX_API_KEY") != "" || m.host.Getenv("OPENAI_API_KEY") != "" {
			return "", ErrUnsupported
		}
		root = m.host.Getenv("CODEX_HOME")
		if root == "" {
			if m.host.HomeDir() == "" {
				return "", ErrUnsupported
			}
			root = filepath.Join(m.host.HomeDir(), ".codex")
		}
	}
	if !filepath.IsAbs(root) {
		return "", ErrUnsupported
	}
	return filepath.Clean(root), nil
}

// Only file-backed auth is supported. Recognize the simple native top-level
// setting, and reject ambiguous spellings rather than silently bypass keyring.
func codexFileMode(root string) error {
	raw, err := codexRead(filepath.Join(root, "config.toml"))
	if errors.Is(err, ErrNotSignedIn) {
		return nil
	}
	if err != nil {
		return err
	}
	scan := bufio.NewScanner(bytes.NewReader(raw))
	top := true
	seen := false
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			top = false
			continue
		}
		pair := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(pair[0])
		// Escaped TOML keys need a full parser. Refuse them instead of
		// overlooking a keyring setting written with Unicode escapes.
		if strings.Contains(key, `\`) {
			return ErrUnsupported
		}
		key = strings.Trim(key, "\"'")
		if key != "cli_auth_credentials_store" {
			continue
		}
		if !top || len(pair) != 2 || seen {
			return ErrUnsupported
		}
		seen = true
		value := strings.TrimSpace(strings.SplitN(pair[1], "#", 2)[0])
		if value != "\"file\"" && value != "'file'" {
			return ErrUnsupported
		}
	}
	if scan.Err() != nil {
		return ErrSchema
	}
	return nil
}

func codexRead(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, ErrNotSignedIn
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("Codex credentials could not be read safely.")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("Codex credentials could not be read.")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, codexMaxAuth+1))
	if err != nil || len(raw) > codexMaxAuth {
		return nil, ErrSchema
	}
	return raw, nil
}

func codexClaims(token string) (map[string]json.RawMessage, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrSchema
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrSchema
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(raw, &claims) != nil || claims == nil {
		return nil, ErrSchema
	}
	return claims, nil
}

// jsonString reads a JSON string value, or "" for anything else.
func jsonString(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }
func codexID(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, ":\r\n\t ")
}

func codexParse(raw []byte) (Captured, error) {
	var doc map[string]json.RawMessage
	if len(raw) > codexMaxAuth || json.Unmarshal(raw, &doc) != nil || doc == nil {
		return Captured{}, ErrSchema
	}
	mode := jsonString(doc["auth_mode"])
	if value, ok := doc["auth_mode"]; ok && string(value) != "null" {
		var explicit string
		if json.Unmarshal(value, &explicit) != nil {
			return Captured{}, ErrSchema
		}
	}
	if mode != "" && mode != "chatgpt" {
		return Captured{}, ErrUnsupported
	}
	for _, key := range []string{"OPENAI_API_KEY", "agent_identity", "personal_access_token", "bedrock_api_key", "bedrock_access_keys"} {
		if value, ok := doc[key]; ok && string(value) != "null" && string(value) != "\"\"" {
			return Captured{}, ErrUnsupported
		}
	}
	var tokens map[string]json.RawMessage
	if json.Unmarshal(doc["tokens"], &tokens) != nil || tokens == nil {
		return Captured{}, ErrNotSignedIn
	}
	idToken, access, refresh := jsonString(tokens["id_token"]), jsonString(tokens["access_token"]), jsonString(tokens["refresh_token"])
	if idToken == "" || access == "" || refresh == "" {
		return Captured{}, ErrSchema
	}
	claims, err := codexClaims(idToken)
	if err != nil {
		return Captured{}, err
	}
	var auth map[string]json.RawMessage
	if json.Unmarshal(claims["https://api.openai.com/auth"], &auth) != nil {
		return Captured{}, ErrNoIdentity
	}
	user := jsonString(auth["chatgpt_user_id"])
	if user == "" {
		user = jsonString(auth["user_id"])
	}
	workspace, claimWorkspace := jsonString(tokens["account_id"]), jsonString(auth["chatgpt_account_id"])
	if workspace == "" {
		workspace = claimWorkspace
	}
	if claimWorkspace != "" && workspace != claimWorkspace {
		return Captured{}, ErrSchema
	}
	if !codexID(user) || !codexID(workspace) {
		return Captured{}, ErrNoIdentity
	}
	var expiry time.Time
	if a, e := codexClaims(access); e == nil {
		var exp int64
		if json.Unmarshal(a["exp"], &exp) == nil && exp > 0 {
			expiry = time.Unix(exp, 0)
		}
	}
	payload, _ := json.Marshal(map[string]json.RawMessage{"auth.json": raw})
	return Captured{Identity: "openai:" + user + ":" + workspace, Label: identityLabel(jsonString(claims["email"]), jsonString(auth["chatgpt_plan_type"])), ExpiresAt: expiry, Credential: store.Credential{Schema: codexAuthSchema, Payload: payload}}, nil
}
func codexUnpack(c store.Credential) ([]byte, Captured, error) {
	if c.Schema != codexAuthSchema {
		return nil, Captured{}, ErrSchema
	}
	var roles map[string]json.RawMessage
	if json.Unmarshal(c.Payload, &roles) != nil || len(roles) != 1 || len(roles["auth.json"]) == 0 {
		return nil, Captured{}, ErrSchema
	}
	parsed, err := codexParse(roles["auth.json"])
	return roles["auth.json"], parsed, err
}
func (m *codexManager) capture(root string) ([]Captured, error) {
	if err := codexFileMode(root); err != nil {
		return nil, err
	}
	raw, err := codexRead(filepath.Join(root, "auth.json"))
	if err != nil {
		return nil, err
	}
	c, err := codexParse(raw)
	if err != nil {
		return nil, err
	}
	return []Captured{c}, nil
}
func (m *codexManager) Capture(ctx context.Context, root string) ([]Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := m.root(root)
	if err != nil {
		return nil, err
	}
	return m.capture(root)
}

func (m *codexManager) Activate(ctx context.Context, integration string, c store.Credential) ([]Captured, error) {
	return m.activate(ctx, integration, c, nil)
}
func (m *codexManager) activate(ctx context.Context, integration string, c store.Credential, refresh *activeRefresh) ([]Captured, error) {
	if integration != "" {
		return nil, ErrSchema
	}
	raw, _, err := codexUnpack(c)
	if err != nil {
		return nil, err
	}
	root, err := m.root("")
	if err != nil {
		return nil, err
	}
	if err = codexFileMode(root); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, errors.New("Codex credential directory could not be created.")
	}
	lock, err := os.OpenFile(filepath.Join(root, ".dyna-auth.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("Codex credential lock could not be opened.")
	}
	defer lock.Close()
	// Native Codex does not honor this lock. It serializes Dyna writers only.
	for {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return nil, errors.New("Codex credential lock failed.")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	previous, err := m.capture(root)
	if err != nil && !errors.Is(err, ErrNotSignedIn) {
		return nil, err
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
		raw, _, e = codexUnpack(next.Credential)
		if e != nil {
			return previous, e
		}
	}
	if err = ctx.Err(); err != nil {
		return previous, err
	}
	if info, e := os.Lstat(filepath.Join(root, "auth.json")); e == nil && !info.Mode().IsRegular() {
		return previous, errors.New("Codex credential target is unsafe.")
	} else if e != nil && !os.IsNotExist(e) {
		return previous, errors.New("Codex credential target could not be inspected.")
	}
	f, err := os.CreateTemp(root, ".dyna-auth-*")
	if err != nil {
		return previous, errors.New("Codex credentials could not be written.")
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(root, "auth.json"))
	}
	if err != nil {
		return previous, errors.New("Codex credentials could not be replaced.")
	}
	dir, err := os.Open(root)
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	if err != nil {
		return previous, ErrPartialActivation
	}
	return previous, nil
}

func (m *codexManager) Refresh(ctx context.Context, c Captured) (Captured, error) {
	if err := ctx.Err(); err != nil {
		return c, err
	}
	raw, parsed, err := codexUnpack(c.Credential)
	if err != nil {
		return Captured{}, err
	}
	if c.Identity != parsed.Identity || c.Integration != "" {
		return Captured{}, ErrSchema
	}
	if parsed.ExpiresAt.IsZero() {
		return Captured{}, ErrUnsupported
	}
	if time.Until(parsed.ExpiresAt) > 5*time.Minute {
		return c, nil
	}
	var doc, tokens map[string]json.RawMessage
	_ = json.Unmarshal(raw, &doc)
	_ = json.Unmarshal(doc["tokens"], &tokens)
	body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "client_id": "app_EMoamEEZ73f0CkXaXp7hrann", "refresh_token": jsonString(tokens["refresh_token"])})
	endpoint := m.tokenURL
	if endpoint == "" {
		endpoint = "https://auth.openai.com/oauth/token"
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Captured{}, ErrUnsupported
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 15 * time.Second}
	if m.client != nil {
		client = *m.client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return Captured{}, errors.New("Codex sign-in could not be renewed.")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return Captured{}, &QuotaError{Status: resp.StatusCode, RetryAfter: codexQuotaRetryAfter(resp.Header.Get("Retry-After"))}
	}
	response, err := io.ReadAll(io.LimitReader(resp.Body, codexMaxAuth+1))
	if err != nil || len(response) > codexMaxAuth {
		return Captured{}, ErrSchema
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
		(resp.StatusCode == http.StatusBadRequest && codexInvalidRefreshGrant(response)) {
		return Captured{}, ErrInvalidGrant
	}
	if resp.StatusCode != http.StatusOK {
		return Captured{}, errors.New("Codex sign-in could not be renewed.")
	}
	var next map[string]json.RawMessage
	if json.Unmarshal(response, &next) != nil || jsonString(next["access_token"]) == "" {
		return Captured{}, ErrSchema
	}
	for _, key := range []string{"access_token", "id_token", "refresh_token"} {
		if value, ok := next[key]; ok && string(value) != "null" {
			if jsonString(value) == "" {
				return Captured{}, ErrSchema
			}
			tokens[key] = value
		}
	}
	doc["tokens"], _ = json.Marshal(tokens)
	doc["last_refresh"], _ = json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	raw, _ = json.Marshal(doc)
	renewed, err := codexParse(raw)
	if err != nil {
		return Captured{}, err
	}
	if renewed.Identity != c.Identity {
		return Captured{}, ErrSchema
	}
	renewed.Label = c.Label
	return renewed, nil
}

// A bad request can describe a client or service problem. Only explicit grant
// failures make the saved account require login. Never expose the response body.
func codexInvalidRefreshGrant(raw []byte) bool {
	var body struct {
		Error       json.RawMessage `json:"error"`
		Description string          `json:"error_description"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	code := jsonString(body.Error)
	description := body.Description
	if code == "" {
		var detail struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(body.Error, &detail) != nil {
			return false
		}
		code = detail.Code
		if description == "" {
			description = detail.Message
		}
	}
	if code == "invalid_grant" {
		return true
	}
	if code != "invalid_request" {
		return false
	}
	description = strings.ToLower(description)
	refresh := strings.Contains(description, "refresh token") || strings.Contains(description, "refresh_token")
	return refresh && (strings.Contains(description, "expired") || strings.Contains(description, "revoked"))
}

func (m *codexManager) LoginCommand(root, integration string) ([]string, []string, error) {
	if integration != "" {
		return nil, nil, ErrSchema
	}
	exe := m.host.Executable
	if exe == "" {
		exe = "codex"
	}
	if root == "" {
		return []string{exe, "login"}, nil, nil
	}
	if !filepath.IsAbs(root) {
		return nil, nil, ErrUnsupported
	}
	// Empty values override inherited API keys. File mode is explicit for capture.
	return []string{exe, "-c", "cli_auth_credentials_store=" + strconv.Quote("file"), "login"}, []string{"CODEX_HOME=" + root, "CODEX_API_KEY=", "OPENAI_API_KEY="}, nil
}

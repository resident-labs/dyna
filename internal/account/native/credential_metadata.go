package native

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"dyna-agent/internal/account/store"
)

// CredentialRecoverer reads newer native grants without refreshing or writing.
// ErrNotSignedIn means that no newer, unexpired same-identity grant was found.
type CredentialRecoverer interface {
	RecoverCredential(context.Context, Captured) (Captured, error)
}

// CredentialExpirer validates the saved payload and reads its access-token expiry.
// ErrUnsupported means that this credential has no supported expiry metadata.
type CredentialExpirer interface {
	CredentialExpiry(Captured) (time.Time, error)
}

// CredentialRefreshSupport narrows refresh support to a validated credential.
// Adapters can expose a refresh operation for some login modes without claiming
// that every login mode can use it.
type CredentialRefreshSupport interface {
	SupportsCredentialRefresh(Captured) bool
}

// SupportsRefresh is the shared refresh gate for registry and private accounts.
func SupportsRefresh(m Manager, c Captured) bool {
	if !m.Caps().Refresh {
		return false
	}
	if support, ok := m.(CredentialRefreshSupport); ok {
		return support.SupportsCredentialRefresh(c)
	}
	return true
}

// CredentialUsesAPIKey reports a validated key login, including a key fallback.
func CredentialUsesAPIKey(c Captured) bool {
	if c.Credential.Schema == piNativeFilesSchema {
		_, entry, err := piAccountEntry(c.Credential)
		if err != nil {
			return false
		}
		return entry.Key != "" || entry.Type == "api_key"
	}
	if c.Credential.Schema == openCodeRefSchema {
		ref, err := openCodeUnpack(c.Credential)
		return err == nil && c.Identity == "opencode:"+ref.Integration+":"+ref.CredentialID && c.Integration == ref.Integration && ref.CredentialType == "key"
	}
	if c.Credential.Schema != museAuthSchema {
		return false
	}
	parsed, err := credentialMetadata(c.Credential)
	if err != nil || parsed.Identity != c.Identity {
		return false
	}
	var p struct {
		Meta struct {
			APIKey string `json:"api_key"`
		} `json:"metaCredential"`
	}
	return json.Unmarshal(c.Credential.Payload, &p) == nil && p.Meta.APIKey != ""
}

// ActiveRefresher holds the adapter's activation lock while it captures, refreshes,
// checkpoints, and writes the active credential. The caller holds the store lock.
// Native locks coordinate Dyna only. Stop native writers first.
// A returned credential can contain a rotated grant even when a later step fails.
type ActiveRefresher interface {
	RefreshActive(context.Context, Captured, func(Captured) error) (Captured, error)
}

type activeRefresh struct {
	current, result Captured
	checkpoint      func(Captured) error
	// onlyNewer writes current only when it expires later than the native login.
	onlyNewer bool
}

var errNotNewer = errors.New("the native login is already as new")

func (m *claudeManager) RefreshActive(ctx context.Context, c Captured, checkpoint func(Captured) error) (Captured, error) {
	if checkpoint == nil {
		return c, ErrUnsupported
	}
	parsed, err := credentialMetadata(c.Credential)
	if err != nil {
		return c, err
	}
	if parsed.Identity != c.Identity || parsed.Integration != c.Integration {
		return c, ErrNoIdentity
	}
	r := &activeRefresh{current: c, result: c, checkpoint: checkpoint}
	_, err = m.activate(ctx, c.Integration, c.Credential, r)
	return r.result, err
}

// credentialMetadata derives identity and expiry from the payload, not cached fields.
func credentialMetadata(c store.Credential) (Captured, error) {
	switch c.Schema {
	case "grok/native-files@1":
		parsed := Captured{Identity: "grok:native", Credential: c}
		login, err := grokSavedSession(parsed)
		if err != nil {
			return Captured{}, err
		}
		parsed.Identity = login.identity()
		parsed.Label = login.label()
		parsed.ExpiresAt = login.ExpiresAt
		return parsed, nil
	case claudeSchema:
		_, tok, id, label, err := claudeDecode(c)
		return Captured{Identity: id, Label: label, Credential: c, ExpiresAt: time.UnixMilli(tok.ExpiresAt)}, err
	case piNativeFilesSchema:
		id, entry, err := piAccountEntry(c)
		if err != nil {
			return Captured{}, err
		}
		return Captured{Identity: "pi:" + id, Credential: c, ExpiresAt: piEntryExpiry(entry)}, nil
	case museAuthSchema:
		var p map[string]json.RawMessage
		if len(c.Payload) > museAuthMaxBytes || json.Unmarshal(c.Payload, &p) != nil || len(p) != 1 {
			return Captured{}, ErrSchema
		}
		rows, err := museCaptureMeta(p["metaCredential"])
		if err != nil {
			return Captured{}, err
		}
		return rows[0], nil
	case openCodeRefSchema:
		ref, err := openCodeUnpack(c)
		if err != nil {
			return Captured{}, err
		}
		parsed := Captured{Identity: "opencode:" + ref.Integration + ":" + ref.CredentialID, Integration: ref.Integration, Credential: c}
		if ref.Expires > 0 {
			parsed.ExpiresAt = time.UnixMilli(ref.Expires).UTC()
		}
		return parsed, nil
	case kimiCodeSchema:
		login, err := unpackKimiCodeLogin(c)
		if err != nil {
			return Captured{}, err
		}
		parsed, err := capturedKimiCode(login)
		parsed.Credential = c
		return parsed, err
	}
	return Captured{}, ErrSchema
}

// CredentialExpiry returns zero when expiry is absent or the payload is invalid.
func CredentialExpiry(c store.Credential) time.Time {
	parsed, err := credentialMetadata(c)
	if err != nil {
		return time.Time{}
	}
	return parsed.ExpiresAt
}

// CredentialExpired reports an expired access credential only when no native
// API-key fallback remains. Expiry metadata alone does not invalidate that key.
func CredentialExpired(c Captured, now time.Time) bool {
	// OpenCode owns the live grant. A saved reference can predate native renewal.
	if c.Credential.Schema == openCodeRefSchema {
		return false
	}
	expiry := CredentialExpiry(c.Credential)
	if expiry.IsZero() {
		expiry = c.ExpiresAt
	}
	if expiry.IsZero() || expiry.After(now) {
		return false
	}
	return !CredentialUsesAPIKey(c)
}

func credentialExpiry(c Captured) (time.Time, error) {
	parsed, err := credentialMetadata(c.Credential)
	if err != nil {
		return time.Time{}, err
	}
	if parsed.Identity != c.Identity || parsed.Integration != c.Integration {
		return time.Time{}, ErrNoIdentity
	}
	if parsed.ExpiresAt.IsZero() {
		return time.Time{}, ErrUnsupported
	}
	return parsed.ExpiresAt, nil
}
func (*claudeManager) CredentialExpiry(c Captured) (time.Time, error)   { return credentialExpiry(c) }
func (*museManager) CredentialExpiry(c Captured) (time.Time, error)     { return credentialExpiry(c) }
func (*openCodeManager) CredentialExpiry(c Captured) (time.Time, error) { return credentialExpiry(c) }

// NewerCredential compares validated same-identity payloads by access-token expiry.
// A known expiry takes precedence over an unknown expiry so a saved rotation
// survives a failed native write. This does not prove remote validity.
func NewerCredential(candidate, previous Captured) bool {
	a, err := credentialMetadata(candidate.Credential)
	if err != nil {
		return false
	}
	b, err := credentialMetadata(previous.Credential)
	if err != nil {
		return false
	}
	return candidate.Credential.Schema == previous.Credential.Schema && candidate.Integration == previous.Integration &&
		candidate.Identity == previous.Identity && a.Identity == candidate.Identity && b.Identity == previous.Identity &&
		!a.ExpiresAt.IsZero() && a.ExpiresAt.After(b.ExpiresAt)
}

func recoverNative(ctx context.Context, m Manager, c Captured, now time.Time, extra []Captured) (Captured, error) {
	rows, err := m.Capture(ctx, "")
	if ctx.Err() != nil {
		return c, ctx.Err()
	}
	// An unreadable native location does not prevent recovery from a bound legacy snapshot.
	if err == nil {
		extra = append(extra, rows...)
	}
	best := c
	found := false
	for _, candidate := range extra {
		if NewerCredential(candidate, best) && CredentialExpiry(candidate.Credential).After(now.Add(time.Minute)) {
			best, found = candidate, true
		}
	}
	if !found {
		return c, ErrNotSignedIn
	}
	best.Label = c.Label
	return best, nil
}
func (m *claudeManager) RecoverCredential(ctx context.Context, c Captured) (Captured, error) {
	// TODO: legacy/shim remove later — recovers rotated claude-accounts snapshots until the legacy helper is retired
	rows, _ := ImportLegacyClaude(ctx, m.host)
	return recoverNative(ctx, m, c, directNow(m.now), rows)
}
func (m *museManager) RecoverCredential(ctx context.Context, c Captured) (Captured, error) {
	return recoverNative(ctx, m, c, time.Now(), nil)
}

// CredentialLabel derives the default label from a validated saved credential.
func CredentialLabel(c Captured) string {
	parsed, err := credentialMetadata(c.Credential)
	if err != nil || parsed.Identity != c.Identity || c.Integration != "" {
		return ""
	}
	return parsed.Label
}

func identityLabel(email, plan string) string {
	if email != "" {
		return email
	}
	return plan
}

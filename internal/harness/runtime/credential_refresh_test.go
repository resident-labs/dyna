package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	accountstore "dyna-agent/internal/account/store"
)

type credentialRefreshFixture struct {
	current, next, recovered native.Captured
	supported                bool
	savedCalls, activeCalls  int
	refreshErr, nativeErr    error
	beforeWrite              func(native.Captured) error
}

func (*credentialRefreshFixture) Harness() string { return "pi" }
func (m *credentialRefreshFixture) Caps() native.Caps {
	return native.Caps{Refresh: m.supported}
}
func (m *credentialRefreshFixture) Capture(context.Context, string) ([]native.Captured, error) {
	if m.current.Identity == "" {
		return nil, native.ErrNotSignedIn
	}
	return []native.Captured{m.current}, nil
}
func (*credentialRefreshFixture) Activate(context.Context, string, accountstore.Credential) ([]native.Captured, error) {
	return nil, errors.New("fixture forbids activation")
}
func (*credentialRefreshFixture) LoginCommand(string, string) ([]string, []string, error) {
	return []string{"/fake/pi", "login"}, nil, nil
}
func (m *credentialRefreshFixture) Refresh(context.Context, native.Captured) (native.Captured, error) {
	m.savedCalls++
	return m.next, m.refreshErr
}
func (m *credentialRefreshFixture) RefreshActive(_ context.Context, c native.Captured, save func(native.Captured) error) (native.Captured, error) {
	m.activeCalls++
	if m.refreshErr != nil {
		return c, m.refreshErr
	}
	if err := save(m.next); err != nil {
		return m.next, err
	}
	if m.beforeWrite != nil {
		if err := m.beforeWrite(m.next); err != nil {
			return m.next, err
		}
	}
	if m.nativeErr != nil {
		return m.next, m.nativeErr
	}
	m.current = m.next
	return m.next, nil
}
func (m *credentialRefreshFixture) RecoverCredential(context.Context, native.Captured) (native.Captured, error) {
	if m.recovered.Identity == "" {
		return native.Captured{}, native.ErrNotSignedIn
	}
	return m.recovered, nil
}

// refreshFixtureCredential builds a saved Pi credential for one provider
// entry, so a rotated credential that names another provider is a changed
// identity.
func refreshFixtureCredential(t *testing.T, provider string, expiry time.Time, grant string) native.Captured {
	t.Helper()
	jwt := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
	}
	access := jwt(map[string]int64{"exp": expiry.Unix()})
	expires := expiry.UnixMilli()
	entry, err := json.Marshal(map[string]any{"type": "oauth", "access": access, "refresh": grant, "expires": expires, "accountId": "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := json.Marshal(map[string]json.RawMessage{provider: entry})
	payload, _ := json.Marshal(map[string]json.RawMessage{"auth.json": auth})
	return native.Captured{Identity: "pi:" + provider, Credential: accountstore.Credential{Schema: "pi/native-files@1", Payload: payload}, ExpiresAt: time.UnixMilli(expires)}
}

func setupCredentialRefresh(t *testing.T, active bool) (*Accounts, NativeAccount, *credentialRefreshFixture) {
	t.Helper()
	a := testAccounts(t)
	n, err := a.Create("pi", "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	c := refreshFixtureCredential(t, "openai-codex", time.Now().Add(-time.Minute), "fake-original-grant")
	m := &credentialRefreshFixture{supported: true, next: refreshFixtureCredential(t, "openai-codex", time.Now().Add(time.Hour), "fake-rotated-grant")}
	a.NativeManager = func(string, native.Host) (native.Manager, bool) { return m, true }
	if err = a.transaction(func(st *accountstore.State) error {
		e := st.Find(n.ID)
		e.Identity, e.NeedsLogin = c.Identity, false
		return writeAccountLogin(n.Home, c)
	}, true); err != nil {
		t.Fatal(err)
	}
	if active {
		m.current = c
		if err = a.setActive("pi", n.ID); err != nil {
			t.Fatal(err)
		}
	}
	return a, n, m
}

func TestCredentialRefreshInactiveCoalesces(t *testing.T) {
	a, n, m := setupCredentialRefresh(t, false)
	c, err := readAccountLogin(n.Home)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivateAccountJSON(filepath.Join(n.Home, ".login-pending.json"), c); err != nil {
		t.Fatal(err)
	}
	other := &Accounts{Root: a.Root, NativeManager: a.NativeManager}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, accounts := range []*Accounts{a, other} {
		wg.Go(func() {
			_, err := accounts.RefreshCredentials(context.Background(), n.ID, "/fake/pi")
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if m.savedCalls != 1 || m.activeCalls != 0 || m.current.Identity != "" {
		t.Fatal("refresh calls did not coalesce or changed native login")
	}
	for _, path := range []string{".login.json", ".login-pending.json"} {
		saved, err := readAccountLoginFile(filepath.Join(n.Home, path))
		if err != nil || !bytes.Equal(saved.Credential.Payload, m.next.Credential.Payload) {
			t.Fatal("rotated grant was not saved", path, err)
		}
		checkMode(t, filepath.Join(n.Home, path), 0600)
	}
	index := accountMetadataBytes(t, a)
	if bytes.Contains(index, []byte("fake-rotated-grant")) {
		t.Fatal("index contains a credential")
	}
}

func TestCredentialRefreshActiveCheckpointSurvivesNativeFailure(t *testing.T) {
	a, n, m := setupCredentialRefresh(t, true)
	m.nativeErr = errors.New("fixture native write failed")
	m.beforeWrite = func(next native.Captured) error {
		saved, err := readAccountLogin(n.Home)
		if err != nil || !bytes.Equal(saved.Credential.Payload, next.Credential.Payload) {
			return errors.New("native write preceded snapshot checkpoint")
		}
		return nil
	}
	if _, err := a.RefreshCredentials(context.Background(), n.ID, "/fake/pi"); !errors.Is(err, m.nativeErr) {
		t.Fatal("native failure was lost", err)
	}
	saved, err := readAccountLogin(n.Home)
	if err != nil || !bytes.Equal(saved.Credential.Payload, m.next.Credential.Payload) {
		t.Fatal("native failure lost the rotated grant", err)
	}
	m.nativeErr = nil
	result, err := a.RefreshCredentials(context.Background(), n.ID, "/fake/pi")
	if err != nil || !result.Active || !result.Refreshed || m.activeCalls != 2 || m.savedCalls != 0 {
		t.Fatal("newer checkpoint was not sent for native repair", result, err)
	}
}

func TestCredentialRefreshInvalidGrantMarksLogin(t *testing.T) {
	a, n, m := setupCredentialRefresh(t, false)
	m.refreshErr = native.ErrInvalidGrant
	result, err := a.RefreshCredentials(context.Background(), n.ID, "/fake/pi")
	if !errors.Is(err, native.ErrInvalidGrant) || result.Reason == "" {
		t.Fatal("invalid grant was not reported", result, err)
	}
	st, err := (&accountstore.Store{Root: a.Root}).Read()
	if err != nil || !st.Find(n.ID).NeedsLogin {
		t.Fatal("invalid grant did not persist NeedsLogin", err)
	}
}

func TestCredentialRefreshRecoversBeforeProvider(t *testing.T) {
	a, n, m := setupCredentialRefresh(t, false)
	m.recovered = m.next
	if err := a.transaction(func(st *accountstore.State) error {
		st.Find(n.ID).NeedsLogin = true
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	result, err := a.RefreshCredentials(context.Background(), n.ID, "/fake/pi")
	if err != nil || result.Refreshed || m.savedCalls != 0 || !result.ExpiresAt.Equal(m.next.ExpiresAt) {
		t.Fatal("newer native recovery did not avoid provider refresh", result, err)
	}
	st, err := (&accountstore.Store{Root: a.Root}).Read()
	if err != nil || st.Find(n.ID).NeedsLogin {
		t.Fatal("recovery did not clear NeedsLogin", err)
	}
}

func TestCredentialRefreshUnsupportedReportsExpiry(t *testing.T) {
	a, n, m := setupCredentialRefresh(t, false)
	m.supported = false
	result, err := a.RefreshCredentials(context.Background(), n.ID, "/fake/pi")
	if !errors.Is(err, native.ErrUnsupported) || result.Supported || result.ExpiresAt.IsZero() || result.Reason == "" || m.savedCalls != 0 {
		t.Fatal("unsupported refresh lacks status", result, err)
	}
}

func TestCredentialRefreshRejectsChangedIdentity(t *testing.T) {
	a, n, m := setupCredentialRefresh(t, false)
	before, err := readAccountLogin(n.Home)
	if err != nil {
		t.Fatal(err)
	}
	m.next = refreshFixtureCredential(t, "zai", time.Now().Add(time.Hour), "fake-wrong-grant")
	if _, err = a.RefreshCredentials(context.Background(), n.ID, "/fake/pi"); !errors.Is(err, native.ErrNoIdentity) {
		t.Fatal("changed identity was accepted", err)
	}
	after, err := readAccountLogin(n.Home)
	if err != nil || !bytes.Equal(before.Credential.Payload, after.Credential.Payload) {
		t.Fatal("changed identity replaced the saved login", err)
	}
}

func TestCredentialRefreshWaitsForTurnLease(t *testing.T) {
	a, n, m := setupCredentialRefresh(t, true)
	release, err := a.TurnLease(context.Background(), "pi")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err = a.RefreshCredentials(ctx, n.ID, "/fake/pi"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("refresh bypassed a native turn", err)
	}
	if m.activeCalls != 0 || m.savedCalls != 0 {
		t.Fatal("refresh ran while a native turn held its lease")
	}
}

type credentialGateFixture struct{ *credentialRefreshFixture }

func (credentialGateFixture) SupportsCredentialRefresh(native.Captured) bool { return false }

func TestCredentialRefreshDeclinedLoginDoesNotReachProvider(t *testing.T) {
	for _, active := range []bool{false, true} {
		a, n, m := setupCredentialRefresh(t, active)
		a.NativeManager = func(string, native.Host) (native.Manager, bool) { return credentialGateFixture{m}, true }
		before, err := readAccountLogin(n.Home)
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.RefreshCredentials(context.Background(), n.ID, "/fake/pi")
		if !errors.Is(err, native.ErrUnsupported) || result.Supported || m.savedCalls != 0 || m.activeCalls != 0 {
			t.Fatalf("active=%v result=%+v err=%v", active, result, err)
		}
		after, err := readAccountLogin(n.Home)
		if err != nil || !bytes.Equal(before.Credential.Payload, after.Credential.Payload) {
			t.Fatal("declined refresh changed credential", err)
		}
	}
}

func TestUnsupportedExpiredPrivateLoginNeedsSignIn(t *testing.T) {
	for _, h := range []string{"muse"} {
		t.Run(h, func(t *testing.T) {
			a := &Accounts{Root: filepath.Join(t.TempDir(), "accounts")}
			n, err := a.Create(h, "Fixture")
			if err != nil {
				t.Fatal(err)
			}
			for _, expired := range []bool{true, false} {
				expiry := time.Now().Add(time.Hour)
				if expired {
					expiry = time.Now().Add(-time.Hour)
				}
				var payload []byte
				identity, schema := "meta:email:fixture@example.test", "muse/auth-file@1"
				payload, _ = json.Marshal(map[string]any{"metaCredential": map[string]any{"mechanism": "oauth", "access_token": "fixture", "user_email": "fixture@example.test", "expires_at": expiry.Unix()}})
				c := native.Captured{Identity: identity, Credential: accountstore.Credential{Schema: schema, Payload: payload}}
				if err := writeAccountLogin(n.Home, c); err != nil {
					t.Fatal(err)
				}
				store := &accountstore.Store{Root: a.Root}
				if err := store.Update(func(st *accountstore.State) error {
					e := st.Find(n.ID)
					e.Identity = identity
					e.NeedsLogin = false
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				before, _ := os.ReadFile(filepath.Join(n.Home, ".login.json"))
				rows, err := a.List()
				want := "ready"
				if expired {
					want = "not signed in"
				}
				if err != nil || len(rows) != 1 || rows[0].Status != want || rows[0].ExpiresAt == nil || rows[0].ExpiresAt.Unix() != expiry.Unix() {
					t.Fatalf("expired=%v: rows=%+v, err=%v", expired, rows, err)
				}
				after, _ := os.ReadFile(filepath.Join(n.Home, ".login.json"))
				if !bytes.Equal(before, after) {
					t.Fatal("status read changed native credentials")
				}

				var payloadWithKey map[string]map[string]any
				if err := json.Unmarshal(c.Credential.Payload, &payloadWithKey); err != nil {
					t.Fatal(err)
				}
				role, key := "auth.json", "apiKey"
				if h == "muse" {
					role, key = "metaCredential", "api_key"
				}
				payloadWithKey[role][key] = "fixture-key"
				c.Credential.Payload, _ = json.Marshal(payloadWithKey)
				if err := writeAccountLogin(n.Home, c); err != nil {
					t.Fatal(err)
				}
				rows, err = a.List()
				if err != nil || rows[0].Status != "ready" {
					t.Fatalf("API-key fallback lost: %+v %v", rows, err)
				}
			}
		})
	}
}

func TestPrivateHomeRefreshForwardsCredentialGateAndCheckpoint(t *testing.T) {
	_, n, fixture := setupCredentialRefresh(t, false)
	scope := filepath.Join(n.Home, "private-runtime")
	if err := os.MkdirAll(scope, 0700); err != nil {
		t.Fatal(err)
	}
	fixture.current = refreshFixtureCredential(t, "openai-codex", time.Now().Add(-time.Minute), "old")
	wrapper := checkedHomeManager{Manager: fixture, harness: "muse", scope: scope}
	if !native.SupportsRefresh(wrapper, fixture.current) {
		t.Fatal("wrapper lost supported refresh")
	}
	checked := false
	next, err := wrapper.RefreshActive(context.Background(), fixture.current, func(c native.Captured) error {
		checked = true
		if !bytes.Equal(c.Credential.Payload, fixture.next.Credential.Payload) {
			t.Fatal("wrong checkpoint credential")
		}
		return nil
	})
	if err != nil || !checked || !bytes.Equal(next.Credential.Payload, fixture.next.Credential.Payload) {
		t.Fatal("active refresh was not forwarded", err)
	}
	wrapper.Manager = credentialGateFixture{fixture}
	if native.SupportsRefresh(wrapper, fixture.current) {
		t.Fatal("wrapper lost declined refresh")
	}
	calls := fixture.activeCalls
	if _, err = wrapper.RefreshActive(context.Background(), fixture.current, func(native.Captured) error { t.Fatal("declined refresh checkpoint ran"); return nil }); !errors.Is(err, native.ErrUnsupported) || calls != fixture.activeCalls {
		t.Fatal("declined refresh reached provider", err)
	}
}

type changingCredentialGateFixture struct {
	*credentialRefreshFixture
	retry bool
}

func (m *changingCredentialGateFixture) SupportsCredentialRefresh(c native.Captured) bool {
	return !bytes.Contains(c.Credential.Payload, []byte("declined"))
}
func (m *changingCredentialGateFixture) RecoverCredential(ctx context.Context, c native.Captured) (native.Captured, error) {
	if m.retry && m.activeCalls+m.savedCalls == 0 {
		return native.Captured{}, native.ErrNotSignedIn
	}
	return m.credentialRefreshFixture.RecoverCredential(ctx, c)
}
func TestPrivateRefreshGateChecksChangedCredential(t *testing.T) {
	for _, mode := range []string{"native", "recovered", "retry-active", "retry-inactive"} {
		t.Run(mode, func(t *testing.T) {
			active := mode == "native" || mode == "retry-active"
			a, n, m := setupCredentialRefresh(t, active)
			gate := &changingCredentialGateFixture{credentialRefreshFixture: m, retry: mode == "retry-active" || mode == "retry-inactive"}
			a.NativeManager = func(string, native.Host) (native.Manager, bool) { return gate, true }
			declined := refreshFixtureCredential(t, "openai-codex", time.Now().Add(-30*time.Second), "declined")
			if mode == "native" {
				m.current = declined
			} else {
				m.recovered = declined
			}
			if gate.retry {
				m.refreshErr = native.ErrInvalidGrant
			}
			result, err := a.RefreshCredentials(context.Background(), n.ID, "/fake/pi")
			if !errors.Is(err, native.ErrUnsupported) || result.Supported {
				t.Fatal("changed credential was not declined", result, err)
			}
			wantCalls := 0
			if gate.retry {
				wantCalls = 1
			}
			if m.activeCalls+m.savedCalls != wantCalls {
				t.Fatal("declined credential reached refresh", m.activeCalls, m.savedCalls)
			}
			saved, err := readAccountLogin(n.Home)
			if err != nil || !bytes.Equal(saved.Credential.Payload, declined.Credential.Payload) {
				t.Fatal("changed credential was not checkpointed", err)
			}
		})
	}
}

func TestOpenCodeSavedExpiryDoesNotInvalidateNativeRenewal(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("opencode", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	identity := "opencode:opencode-go:fixture"
	c := native.Captured{Identity: identity, Integration: "opencode-go", Credential: accountstore.Credential{Schema: "opencode/v2-ref@1", Payload: json.RawMessage(`{"ref":{"integration":"opencode-go","credentialID":"fixture","scope":{},"credentialType":"oauth","expires":1}}`)}}
	if err := writeAccountLogin(n.Home, c); err != nil {
		t.Fatal(err)
	}
	if err := (&accountstore.Store{Root: a.Root}).Update(func(s *accountstore.State) error {
		e := s.Find(n.ID)
		e.Identity = identity
		e.NeedsLogin = false
		e.Integration = c.Integration
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := a.Find(n.ID)
	if err != nil || got.Status != "ready" {
		t.Fatalf("native renewal invalidated by reference expiry: %+v %v", got, err)
	}
}

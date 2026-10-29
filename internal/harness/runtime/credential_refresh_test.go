package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	return setupCredentialRefreshFor(t, "pi", active)
}

// setupCredentialRefreshFor keeps the same fixture on another private harness,
// so a test can cover a login that lives in the shared harness home.
func setupCredentialRefreshFor(t *testing.T, harness string, active bool) (*Accounts, NativeAccount, *credentialRefreshFixture) {
	t.Helper()
	a := testAccounts(t)
	n, err := a.Create(harness, "Fixture")
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
		if err = a.setActive(harness, n.ID); err != nil {
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

func TestCredentialRefreshRecordsRenewalResults(t *testing.T) {
	privateErr := errors.New("fixture-private-provider-error")
	for _, tc := range []struct {
		name    string
		err     error
		want    accountstore.RenewalResult
		wantErr error
	}{
		{name: "renewed", want: accountstore.RenewalResultRenewed},
		{name: "no credential", err: native.ErrNotSignedIn, want: accountstore.RenewalResultNoCredential, wantErr: native.ErrNotSignedIn},
		{name: "invalid grant", err: native.ErrInvalidGrant, want: accountstore.RenewalResultInvalidGrant, wantErr: native.ErrInvalidGrant},
		{name: "transient", err: privateErr, want: accountstore.RenewalResultTransient, wantErr: privateErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, n, m := setupCredentialRefresh(t, false)
			m.refreshErr = tc.err
			_, err := a.RefreshCredentials(t.Context(), n.ID, "/fake/pi")
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("refresh error = %v, want %v", err, tc.wantErr)
			}
			latest, err := (&accountstore.Store{Root: a.Root}).LatestRenewals(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(latest) != 1 {
				t.Fatalf("renewal rows = %+v, want one", latest)
			}
			got := latest[n.ID]
			if got.AccountID != n.ID || got.Harness != n.Harness || got.Provider != n.Provider || got.Result != tc.want || got.AttemptedAt.IsZero() {
				t.Fatalf("renewal = %+v, want result %q for account %+v", got, tc.want, n)
			}
			if tc.want == accountstore.RenewalResultRenewed && got.Reason != "" {
				t.Fatalf("successful renewal reason = %q", got.Reason)
			}
			if tc.want != accountstore.RenewalResultRenewed && got.Reason == "" {
				t.Fatal("failed renewal has no reason")
			}
			if strings.Contains(got.Reason, "fixture-private") {
				t.Fatalf("renewal reason leaked provider text: %q", got.Reason)
			}
		})
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

// A login in the shared harness home is renewed only after its turns end: the
// renewal writes the file those turns read. The gate is the seam that waits, so
// the test cancels the call from the gate itself instead of using a deadline.
func TestCredentialRefreshWaitsForTurnLease(t *testing.T) {
	a, n, m := setupCredentialRefreshFor(t, "kimi-code", true)
	release, err := a.TurnLease(context.Background(), "kimi-code")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gated := false
	a.SwitchGate = func(ctx context.Context, _ string) (func(), error) {
		gated = true
		cancel()
		return nil, ctx.Err()
	}
	if _, err = a.RefreshCredentials(ctx, n.ID, "/fake/pi"); !gated || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the refresh gated behind the running turn", err)
	}
	if m.activeCalls != 0 || m.savedCalls != 0 {
		t.Fatal("refresh ran while a native turn held its lease")
	}
}

// A Pi login lives in the account's own home, where Pi renews it under its own
// lock during a turn, so the usage refresh renews it while Pi turns keep running
// instead of waiting for every one of them to end. That wait is what made every
// Pi provider report a deadline while 64 turns were being served, and it also
// held the harness admission lock that every new turn needs, so a turn start
// waited behind the refresh and produced no output.
func TestPiRefreshDoesNotDrainRunningTurns(t *testing.T) {
	a := testAccounts(t)
	n, err := a.CreateProvider("pi", accountstore.ProviderChatGPT, accountstore.KindOAuth, "ChatGPT")
	if err != nil {
		t.Fatal(err)
	}
	writePiGrants(t, n, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "stale-access", Refresh: "grant", Expires: time.Now().Add(-time.Minute).UnixMilli(), AccountID: "workspace"}})
	if n, err = a.Capture(context.Background(), n.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err = a.setActiveAccount(n); err != nil {
		t.Fatal(err)
	}
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	piRenewRunner = func(_ context.Context, home, provider, executable string) error {
		writePiGrants(t, n, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "fresh-access", Refresh: "grant", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "workspace"}})
		return nil
	}
	a.SwitchGate = func(context.Context, string) (func(), error) {
		t.Error("the Pi refresh drained the running turns")
		return nil, errors.New("the Pi refresh took the switch gate")
	}
	release, err := a.TurnLease(context.Background(), "pi")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := a.RefreshCredentials(ctx, n.ID, "/fake/pi/bin/pi")
	if err != nil {
		t.Fatalf("refresh while Pi turns run: %v", err)
	}
	if time.Until(result.ExpiresAt) < 30*time.Minute {
		t.Fatalf("expiry = %v, want the renewed grant", result.ExpiresAt)
	}
	entries, err := readPiAuthEntries(filepath.Join(n.Home, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(entries["openai-codex"]); !strings.Contains(got, "fresh-access") {
		t.Fatalf("auth.json = %s, want the renewed token", got)
	}
}

// A search runs inside a Pi turn that holds its lease. The one ChatGPT reader
// must renew the due grant without waiting for the switch gate, which waits for
// every Pi turn, including the caller's own, and it must return the renewed
// token. The Pi renewal itself is the seam the account refresh already uses, so
// the test writes the fresh entry where Pi writes it.
func TestChatGPTGrantReturnsTheRenewedToken(t *testing.T) {
	a := testAccounts(t)
	n, err := a.CreateProvider("pi", accountstore.ProviderChatGPT, accountstore.KindOAuth, "ChatGPT")
	if err != nil {
		t.Fatal(err)
	}
	writePiGrants(t, n, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "stale-access", Refresh: "grant", Expires: time.Now().Add(-time.Hour).UnixMilli(), AccountID: "workspace"}})
	if n, err = a.Capture(context.Background(), n.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err = a.setActiveAccount(n); err != nil {
		t.Fatal(err)
	}
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	renewals := 0
	piRenewRunner = func(_ context.Context, home, provider, executable string) error {
		renewals++
		writePiGrants(t, n, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "fresh-access", Refresh: "grant", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "workspace"}})
		return nil
	}
	// The managed Pi copy is only named to the renewal, which the runner stands
	// for; the read of the renewed file goes through the auth commands.
	installFakePiAuth(t, a)
	// A reader that regressed to the draining refresh would take this gate and
	// wait behind the lease held below. Failing at once keeps that regression a
	// fast, deterministic failure instead of a hung test.
	a.SwitchGate = func(context.Context, string) (func(), error) {
		t.Error("the live reader took the switch gate that its own turn holds")
		return nil, errors.New("the switch gate was taken during a live turn")
	}
	release, err := a.TurnLease(context.Background(), "pi")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	grant, err := a.ChatGPTGrant(ctx, "", "")
	if err != nil {
		t.Fatalf("grant under a held turn lease: %v", err)
	}
	if renewals != 1 {
		t.Fatalf("renewals = %d, want one", renewals)
	}
	if grant.Access != "fresh-access" || grant.Account != "workspace" {
		t.Fatalf("grant = %+v, want the token the renewal wrote", grant)
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

func TestPrivateHomeRefreshForwardsCredentialGateAndCheckpoint(t *testing.T) {
	_, n, fixture := setupCredentialRefresh(t, false)
	scope := filepath.Join(n.Home, "private-runtime")
	if err := os.MkdirAll(scope, 0700); err != nil {
		t.Fatal(err)
	}
	fixture.current = refreshFixtureCredential(t, "openai-codex", time.Now().Add(-time.Minute), "old")
	wrapper := checkedHomeManager{Manager: fixture, harness: "claude-code", scope: scope}
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

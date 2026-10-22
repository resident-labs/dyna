package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// agyOfflineManager fails the test on any network call: capture, switching and
// usage read the identity recorded at sign-in.
func agyOfflineManager(t *testing.T) *antigravityManager {
	t.Helper()
	return &antigravityManager{host: testHost(t, nil), client: directTestClient(func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected network call to %s", r.URL)
		return nil, errors.New("network forbidden")
	})}
}

// agyACPTestAccount writes an ACP grant and its recorded identity into home.
func agyACPTestAccount(t *testing.T, home, sub string) {
	t.Helper()
	testWrite(t, filepath.Join(home, AntigravityACPTokenFile), json.RawMessage(agyACPTestToken(sub)))
	testWrite(t, filepath.Join(home, AntigravityAccountFile), map[string]string{"identity": "google:" + sub, "label": sub + "@example.test"})
}

func agyACPTestSaved(t *testing.T, sub string) Captured {
	t.Helper()
	c, err := agyACPCaptured(agyACPTestToken(sub), agyACPAccount{Identity: "google:" + sub, Label: sub + "@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAntigravityCaptureReadsTheRecordedIdentityWithoutNetwork(t *testing.T) {
	m := agyOfflineManager(t)
	agyACPTestAccount(t, m.host.Home, "source")
	rows, err := m.Capture(context.Background(), "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("capture failed: %v", err)
	}
	if rows[0].Identity != "google:source" || rows[0].Label != "source@example.test" || rows[0].Credential.Schema != agyACPSchema {
		t.Fatalf("capture named %+v", rows[0])
	}
	if got := CredentialLabel(rows[0]); got != "source@example.test" {
		t.Fatalf("saved label %q", got)
	}
}

// The agy CLI token is no longer a sign-in: a home that holds only that token
// is not signed in, and capture never reads it.
func TestAntigravityCaptureIgnoresTheCLIToken(t *testing.T) {
	m := agyOfflineManager(t)
	testWrite(t, filepath.Join(m.host.Home, ".gemini/antigravity-cli/antigravity-oauth-token"), json.RawMessage(agyTestWrapper(t, "cli")))
	if _, err := m.Capture(context.Background(), ""); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("a CLI token alone was captured: %v", err)
	}
	// A grant without its recorded identity is not a saved sign-in either.
	testWrite(t, filepath.Join(m.host.Home, AntigravityACPTokenFile), json.RawMessage(agyACPTestToken("cli")))
	if _, err := m.Capture(context.Background(), ""); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("a grant without its identity was captured: %v", err)
	}
}

// An ACP grant alone is a complete account: activation writes the grant and
// its identity, removes the outgoing account's files and makes no network call.
func TestAntigravityACPGrantAloneActivates(t *testing.T) {
	m := agyOfflineManager(t)
	agyACPTestAccount(t, m.host.Home, "source")
	previous, err := m.Activate(context.Background(), "", agyACPTestSaved(t, "target").Credential)
	if err != nil || len(previous) != 1 || previous[0].Identity != "google:source" {
		t.Fatalf("activation lost the outgoing account: %v %v", previous, err)
	}
	rows, err := m.Capture(context.Background(), "")
	if err != nil || rows[0].Identity != "google:target" {
		t.Fatalf("target not active: %v %v", rows, err)
	}
	token, err := os.ReadFile(filepath.Join(m.host.Home, AntigravityACPTokenFile))
	if err != nil || !bytes.Equal(token, agyACPTestToken("target")) {
		t.Fatal("activation did not write the target grant")
	}
	for _, name := range []string{AntigravityACPTokenFile, AntigravityAccountFile} {
		if info, err := os.Stat(filepath.Join(m.host.Home, name)); err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s is not private: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(m.host.Home, ".gemini")); !os.IsNotExist(err) {
		t.Fatal("activation wrote an agy CLI token")
	}
}

func TestAntigravityACPPayloadRefusesMissingOrBadEntries(t *testing.T) {
	for name, payload := range map[string]string{
		"no identity":    `{"label":"x","token":` + string(agyACPTestToken("x")) + `}`,
		"cli identity":   `{"identity":"x","token":` + string(agyACPTestToken("x")) + `}`,
		"no token":       `{"identity":"google:x"}`,
		"bad token":      `{"identity":"google:x","token":{"client_id":"x"}}`,
		"unknown entry":  `{"identity":"google:x","token":` + string(agyACPTestToken("x")) + `,"other":1}`,
		"not an object ": `[]`,
	} {
		m := agyOfflineManager(t)
		c := Captured{Identity: "google:x", Credential: agyACPTestSaved(t, "x").Credential}
		c.Credential.Payload = json.RawMessage(payload)
		if _, err := m.Activate(context.Background(), "", c.Credential); !errors.Is(err, ErrSchema) {
			t.Errorf("%s was accepted: %v", name, err)
		}
	}
}

// Accounts saved before ACP-only sign-in carry the agy CLI wrapper. One with
// an ACP grant still activates from its saved identity; one without must sign
// in again.
func TestAntigravityLegacyPayloadActivatesOnlyWithAnACPGrant(t *testing.T) {
	m := agyOfflineManager(t)
	legacy := agyACPTestCredential(t, "legacy", agyACPTestToken("legacy"))
	if _, err := m.Activate(context.Background(), "", legacy.Credential); err != nil {
		t.Fatal(err)
	}
	rows, err := m.Capture(context.Background(), "")
	if err != nil || rows[0].Identity != "google:legacy" || rows[0].Credential.Schema != agyACPSchema {
		t.Fatalf("a legacy account with an ACP grant did not activate: %v %v", rows, err)
	}
	cliOnly := agyACPTestCredential(t, "cli", nil)
	if _, err := m.Activate(context.Background(), "", cliOnly.Credential); err == nil || !strings.Contains(err.Error(), "Sign in") {
		t.Fatalf("a CLI-only account activated: %v", err)
	}
}

func TestAntigravityACPAccountUsageIsUnavailable(t *testing.T) {
	m := agyOfflineManager(t)
	saved := agyACPTestSaved(t, "usage")
	if _, err := m.ProbeSaved(context.Background(), saved); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("usage of an ACP-only account was not reported unavailable: %v", err)
	}
	if next, err := m.Refresh(context.Background(), saved); err != nil || !bytes.Equal(next.Credential.Payload, saved.Credential.Payload) {
		t.Fatalf("refresh changed an ACP-only account: %v", err)
	}
}

// Sign-in records the identity and label once, from the grant it verified.
func TestAntigravityACPLoginRecordsTheIdentity(t *testing.T) {
	login, _ := agyFakeACPLogin(t, "LINK="+agyTestAuthLink, "WRITE=1", "TOKEN="+string(agyACPTestToken("login")))
	login.Expect = ""
	if err := login.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := (&antigravityManager{host: Host{Home: login.Home}}).Capture(context.Background(), login.Home)
	if err != nil || rows[0].Identity != "google:login" || rows[0].Label != "fake@example.test" {
		t.Fatalf("sign-in did not record the identity: %v %v", rows, err)
	}
}

// If Google cannot confirm the account, the sign-in fails plainly and nothing
// is saved.
func TestAntigravityACPLoginSavesNothingWhenUserinfoFails(t *testing.T) {
	login, _ := agyFakeACPLogin(t, "LINK="+agyTestAuthLink, "WRITE=1", "TOKEN="+string(agyACPTestToken("login")))
	login.Expect = ""
	login.Client = directTestClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == agyTokenURL {
			return directTestResponse(200, `{"access_token":"fake-access","expires_in":3599}`), nil
		}
		return directTestResponse(500, ""), nil
	})
	if err := login.Run(context.Background()); err == nil {
		t.Fatal("a sign-in without a confirmed Google account succeeded")
	}
	for _, name := range []string{AntigravityACPTokenFile, AntigravityAccountFile} {
		if _, err := os.Stat(filepath.Join(login.Home, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was kept: %v", name, err)
		}
	}
}

// A sign-in starts from an empty grant, so the outgoing account's grant can
// never be mistaken for the new sign-in.
func TestAntigravityACPLoginNeverReusesTheOutgoingGrant(t *testing.T) {
	login, _ := agyFakeACPLogin(t, "LINK="+agyTestAuthLink)
	login.Expect = ""
	agyACPTestAccount(t, login.Home, "outgoing")
	if err := login.Run(context.Background()); err == nil {
		t.Fatal("the outgoing grant was accepted as a new sign-in")
	}
	if _, err := os.Stat(filepath.Join(login.Home, AntigravityAccountFile)); !os.IsNotExist(err) {
		t.Fatalf("the outgoing identity was kept: %v", err)
	}
}

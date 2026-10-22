package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func agyACPTestToken(sub string) []byte {
	return []byte(`{"client_id":"fake-acp-client-` + sub + `","client_secret":"fake-acp-secret-` + sub + `","refresh_token":"fake-acp-refresh-` + sub + `"}`)
}

func agyACPTestPath(m *antigravityManager) string {
	return filepath.Join(m.host.HomeDir(), AntigravityACPTokenFile)
}

func agyACPTestWrite(t *testing.T, m *antigravityManager, raw []byte) {
	t.Helper()
	path := agyACPTestPath(m)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func agyACPTestCredential(t *testing.T, sub string, acp []byte) Captured {
	t.Helper()
	c, err := agyAccount(agyTestWrapper(t, sub))
	if err != nil {
		t.Fatal(err)
	}
	if acp != nil {
		c.Credential.Payload = agyPack(agyTestWrapper(t, sub), acp)
	}
	return c
}

func TestAntigravityCaptureIncludesACPToken(t *testing.T) {
	m := agyTestManager(t, &agyFakeStore{raw: agyTestWrapper(t, "source")})
	token := agyACPTestToken("source")
	agyACPTestWrite(t, m, token)
	rows, err := m.Capture(context.Background(), "")
	if err != nil || len(rows) != 1 {
		t.Fatal("capture failed", err)
	}
	cli, acp, err := agyPayload(rows[0].Credential.Payload)
	if err != nil || !bytes.Equal(acp, token) || !sameJSON(cli, agyTestWrapper(t, "source")) {
		t.Fatal("capture dropped the CLI wrapper or the ACP token", err)
	}
}

func TestAntigravityCaptureWithoutACPTokenKeepsCLIPayload(t *testing.T) {
	m := agyTestManager(t, &agyFakeStore{raw: agyTestWrapper(t, "source")})
	rows, err := m.Capture(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]json.RawMessage
	if json.Unmarshal(rows[0].Credential.Payload, &p) != nil || len(p) != 1 || p[agyRole] == nil {
		t.Fatal("a home without an ACP token must capture exactly the CLI entry")
	}
}

func TestAntigravityCaptureRejectsIncompleteACPToken(t *testing.T) {
	m := agyTestManager(t, &agyFakeStore{raw: agyTestWrapper(t, "source")})
	agyACPTestWrite(t, m, []byte(`{"client_id":"fake","refresh_token":"fake"}`))
	if _, err := m.Capture(context.Background(), ""); !errors.Is(err, ErrSchema) {
		t.Fatalf("incomplete ACP token was accepted: %v", err)
	}
}

func TestAntigravityActivateWritesPrivateACPTokenAndRemovesStaleOne(t *testing.T) {
	m := agyTestManager(t, &agyFakeStore{raw: agyTestWrapper(t, "source")})
	agyACPTestWrite(t, m, agyACPTestToken("source"))
	withACP := agyACPTestCredential(t, "target", agyACPTestToken("target"))
	if _, err := m.Activate(context.Background(), "", withACP.Credential); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(agyACPTestPath(m))
	info, statErr := os.Stat(agyACPTestPath(m))
	if err != nil || statErr != nil || !bytes.Equal(got, agyACPTestToken("target")) || info.Mode().Perm() != 0600 {
		t.Fatal("activation did not write the ACP token with a private mode")
	}
	withoutACP := agyACPTestCredential(t, "other", nil)
	if _, err := m.Activate(context.Background(), "", withoutACP.Credential); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agyACPTestPath(m)); !os.IsNotExist(err) {
		t.Fatal("an account without an ACP token inherited the previous account's token")
	}
}

func TestAntigravityActivateRollbackRestoresACPToken(t *testing.T) {
	s := &agyFakeStore{raw: agyTestWrapper(t, "source"), failReadAt: 2}
	m := agyTestManager(t, s)
	previousToken := agyACPTestToken("source")
	agyACPTestWrite(t, m, previousToken)
	target := agyACPTestCredential(t, "target", agyACPTestToken("target"))
	_, err := m.Activate(context.Background(), "", target.Credential)
	if !errors.Is(err, ErrPartialActivation) {
		t.Fatalf("verification failure was not reported: %v", err)
	}
	got, readErr := os.ReadFile(agyACPTestPath(m))
	if readErr != nil || !bytes.Equal(got, previousToken) {
		t.Fatal("rollback did not restore the previous ACP token")
	}
}

func TestAntigravityPayloadRejectsUnknownEntryAndBadACPToken(t *testing.T) {
	cli := agyTestWrapper(t, "source")
	for name, payload := range map[string]map[string]json.RawMessage{
		"unknown entry": {agyRole: cli, "file:other": json.RawMessage(`{}`)},
		"bad acp":       {agyRole: cli, agyACPRole: json.RawMessage(`{"client_id":"x"}`)},
		"acp only":      {agyACPRole: agyACPTestToken("source")},
	} {
		raw, _ := json.Marshal(payload)
		if _, _, err := agyPayload(raw); !errors.Is(err, ErrSchema) {
			t.Errorf("%s was accepted: %v", name, err)
		}
	}
}

func TestSavedAntigravityRefreshKeepsACPToken(t *testing.T) {
	now := time.Now().UTC()
	c := directTestAgy(t, now.Add(-time.Hour))
	cli, _, err := agyPayload(c.Credential.Payload)
	if err != nil {
		t.Fatal(err)
	}
	token := agyACPTestToken("saved")
	c.Credential.Payload = agyPack(cli, token)
	m := agyTestManager(t, &agyFakeStore{})
	m.now = func() time.Time { return now }
	m.clientSecret = func(context.Context) (string, error) { return "fake-client-secret", nil }
	m.client = directTestClient(func(*http.Request) (*http.Response, error) {
		return directTestResponse(200, `{"access_token":"new-access","expires_in":3599,"token_type":"Bearer"}`), nil
	})
	next, err := m.Refresh(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	_, acp, err := agyPayload(next.Credential.Payload)
	if err != nil || !bytes.Equal(acp, token) {
		t.Fatal("CLI refresh dropped the saved ACP token", err)
	}
	if _, _, _, err = savedAgy(next); err != nil {
		t.Fatal("refreshed payload no longer validates", err)
	}
}

// agyFakeACPServer is a shell stand-in for agy_acp_server. It answers
// initialize, prints LINK on stderr, writes the token unless WRITE is empty
// and then answers authenticate.
const agyFakeACPServer = `#!/bin/bash
id_of() { sed -E 's/.*"id":("[^"]*"|[0-9]+).*/\1/' <<<"$1"; }
read -r line
echo "{\"jsonrpc\":\"2.0\",\"id\":$(id_of "$line"),\"result\":{\"protocolVersion\":2}}"
read -r line
read -r line
auth=$(id_of "$line")
[ -n "$LINK" ] && echo "Open the following link to authenticate the ACP server: $LINK" >&2
if [ -n "$WRITE" ]; then
  mkdir -p "$GEMINI_HOME/antigravity-acp"
  printf '%s' "$TOKEN" > "$GEMINI_HOME/antigravity-acp/acp_token.json"
fi
echo "{\"jsonrpc\":\"2.0\",\"id\":$auth,\"result\":{}}"
read -r line
`

func agyFakeACPLogin(t *testing.T, env ...string) (AntigravityACPLogin, *bytes.Buffer) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is required for the fake ACP server")
	}
	dir := t.TempDir()
	server := filepath.Join(dir, "agy_acp_server.par")
	if err := os.WriteFile(server, []byte(agyFakeACPServer), 0700); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	home := filepath.Join(dir, "home")
	return AntigravityACPLogin{
		Server: server, Home: home,
		Env: append([]string{
			"PATH=" + os.Getenv("PATH"),
			"GEMINI_HOME=" + home,
			"TMPDIR=" + filepath.Join(home, "antigravity-acp", "tmp"),
		}, env...), Out: out, Expect: "google:login",
		Client: directTestClient(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case agyTokenURL:
				if err := r.ParseForm(); err != nil || r.Form.Get("client_id") != "fake-acp-client-login" || r.Form.Get("refresh_token") != "fake-acp-refresh-login" {
					return directTestResponse(400, `{"error":"invalid_grant"}`), nil
				}
				return directTestResponse(200, `{"access_token":"fake-access","expires_in":3599}`), nil
			case agyUserInfoURL:
				if r.Header.Get("Authorization") != "Bearer fake-access" {
					return directTestResponse(401, ""), nil
				}
				return directTestResponse(200, `{"id":"login","email":"fake@example.test"}`), nil
			}
			return directTestResponse(404, ""), nil
		}),
	}, out
}

const agyTestAuthLink = "https://accounts.google.com/o/oauth2/v2/auth?client_id=fake&redirect_uri=http%3A%2F%2F127.0.0.1%3A5555%2F&state=fake-state"

func TestAntigravityACPLoginShowsLinkAndRequiresToken(t *testing.T) {
	login, out := agyFakeACPLogin(t, "LINK="+agyTestAuthLink, "WRITE=1", "TOKEN="+string(agyACPTestToken("login")))
	if err := login.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), agyTestAuthLink) {
		t.Fatal("the owner was not shown the sign-in link")
	}
	if strings.Contains(out.String(), "fake-acp-") {
		t.Fatal("login output exposed token contents")
	}
	settings, err := os.ReadFile(filepath.Join(login.Home, AntigravityACPSettingsFile))
	if err != nil || string(settings) != antigravityACPSettings {
		t.Fatal("login did not select oauth-personal in the private home")
	}
	info, err := os.Stat(filepath.Join(login.Home, AntigravityACPSettingsFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("settings file is not private")
	}
}

func TestAntigravityACPLoginFailsWithoutTokenFile(t *testing.T) {
	login, _ := agyFakeACPLogin(t, "LINK="+agyTestAuthLink)
	if err := login.Run(context.Background()); err == nil {
		t.Fatal("login succeeded although the server wrote no token")
	}
}

func TestAntigravityACPLoginFailsOnIncompleteToken(t *testing.T) {
	login, _ := agyFakeACPLogin(t, "WRITE=1", `TOKEN={"client_id":"x"}`)
	if err := login.Run(context.Background()); !errors.Is(err, ErrSchema) {
		t.Fatalf("incomplete token accepted: %v", err)
	}
}

func TestAntigravityACPLoginIgnoresNonLoopbackLink(t *testing.T) {
	evil := "https://accounts.google.com/o/oauth2/v2/auth?redirect_uri=https%3A%2F%2Fevil.example%2F&state=x"
	login, out := agyFakeACPLogin(t, "LINK="+evil, "WRITE=1", "TOKEN="+string(agyACPTestToken("login")))
	if err := login.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "evil.example") {
		t.Fatal("a non-loopback redirect was shown as a sign-in link")
	}
}

func TestAntigravityACPLoginDiscardsTokenForAnotherGoogleAccount(t *testing.T) {
	login, _ := agyFakeACPLogin(t, "WRITE=1", "TOKEN="+string(agyACPTestToken("login")))
	login.Expect = "google:someone-else"
	err := login.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not this Antigravity account") {
		t.Fatalf("a different Google account was accepted: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(login.Home, AntigravityACPTokenFile)); !os.IsNotExist(statErr) {
		t.Fatal("the other account's grant stayed in the home")
	}
}

func TestAntigravityACPLoginNeedsExpectedIdentity(t *testing.T) {
	login, _ := agyFakeACPLogin(t)
	login.Expect = ""
	if err := login.Run(context.Background()); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("login ran without an account identity: %v", err)
	}
}

func TestAntigravityACPLoginRefusesEnvPointingElsewhere(t *testing.T) {
	login, _ := agyFakeACPLogin(t, "LINK="+agyTestAuthLink, "WRITE=1", "TOKEN="+string(agyACPTestToken("login")))
	// A caller that skipped the AntigravityACPEnv overlay would sign in to a
	// home Run cannot read back.
	login.Env = append(login.Env, "GEMINI_HOME="+filepath.Join(t.TempDir(), "elsewhere"))
	if err := login.Run(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("an environment pointing away from Home was accepted: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(login.Home, AntigravityACPTokenFile)); !os.IsNotExist(statErr) {
		t.Fatal("the server ran although the environment did not match Home")
	}
}

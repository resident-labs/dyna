package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/native"
)

func TestNativeHasGrantRejectsLoggedOutMetadata(t *testing.T) {
	for _, value := range []map[string]any{{}, {"user": "fixture"}, {"access_token": ""}} {
		if nativeHasGrant(value) {
			t.Fatal("metadata was treated as a signed-in grant")
		}
	}
	for _, value := range []map[string]any{{"token": "fixture"}, {"provider": map[string]any{"type": "oauth", "access": "fixture"}}} {
		if !nativeHasGrant(value) {
			t.Fatal("native grant was not detected")
		}
	}
}

func TestNativeFilesGrokUsageIsAvailableThroughSavedProber(t *testing.T) {
	adapter, err := Adapter("grok")
	if err != nil {
		t.Fatal(err)
	}
	manager := &nativeFilesManager{adapter: adapter}
	var prober native.SavedProber = manager
	if _, err = prober.ProbeSaved(context.Background(), native.Captured{}); !errors.Is(err, native.ErrSchema) {
		t.Fatal("Grok reader was not reached", err)
	}
	manager.adapter, _ = Adapter("pi")
	if _, err = prober.ProbeSaved(context.Background(), native.Captured{}); !errors.Is(err, native.ErrSchema) {
		t.Fatal("Pi reader was not reached", err)
	}
	manager.adapter, _ = Adapter("claude-code")
	if _, err = prober.ProbeSaved(context.Background(), native.Captured{}); !errors.Is(err, native.ErrUnsupported) {
		t.Fatal("other native files gained unsupported usage", err)
	}
}

const grokOIDCAuth = `{"https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828":{"key":"fixture-session","auth_mode":"oidc","user_id":"fixture-user","oidc_issuer":"https://auth.x.ai","oidc_client_id":"b1a00492-073a-47ea-816f-4c329264a828"}}`

// Saved Grok accounts record grok:<sha256 of issuer and user>. The private
// runtime capture must report the same identity, or activation rejects the login.
func TestNativeFilesGrokCaptureMatchesSavedIdentity(t *testing.T) {
	adapter, err := Adapter("grok")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "auth.json"), []byte(grokOIDCAuth), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := (&nativeFilesManager{adapter: adapter}).Capture(context.Background(), root)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	sum := sha256.Sum256([]byte("https://auth.x.ai\x00fixture-user"))
	if want := "grok:" + hex.EncodeToString(sum[:]); rows[0].Identity != want {
		t.Fatalf("capture identity %q, saved accounts record %q", rows[0].Identity, want)
	}
}

func TestGrokCaptureIdentityLabel(t *testing.T) {
	for _, tc := range []struct{ email, tier, want string }{
		{"person@example.test", "SuperGrok", "person@example.test"},
		{"", "SuperGrok", "SuperGrok"},
		{"", "", ""},
	} {
		root := t.TempDir()
		var auth map[string]map[string]any
		_ = json.Unmarshal([]byte(grokOIDCAuth), &auth)
		for _, login := range auth {
			login["email"] = tc.email
			claims, _ := json.Marshal(map[string]string{"tier": tc.tier})
			login["key"] = "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
		}
		raw, _ := json.Marshal(auth)
		if err := os.WriteFile(filepath.Join(root, "auth.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		m, ok := NativeRuntimeManager("grok", native.Host{})
		if !ok {
			t.Fatal("missing Grok manager")
		}
		captured, err := m.Capture(context.Background(), root)
		if err != nil || len(captured) != 1 || captured[0].Label != tc.want {
			t.Fatal("incorrect Grok capture label", err)
		}
	}
}

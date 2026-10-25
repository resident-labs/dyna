package native

import (
	"encoding/json"
	"testing"
)

func TestGrokIdentityLabel(t *testing.T) {
	for _, tc := range []struct{ email, tier, want string }{
		{"person@example.test", "SuperGrok", "person@example.test"},
		{"", "SuperGrok", "SuperGrok"}, {"", "", ""},
	} {
		c := grokUsageFixture(t)
		var files map[string]json.RawMessage
		var auth map[string]map[string]any
		_ = json.Unmarshal(c.Credential.Payload, &files)
		_ = json.Unmarshal(files["auth.json"], &auth)
		login := auth[grokOIDCIssuer+"::"+grokOIDCClient]
		login["email"] = tc.email
		login["key"] = testJWT(map[string]string{"tier": tc.tier})
		files["auth.json"], _ = json.Marshal(auth)
		c.Credential.Payload, _ = json.Marshal(files)
		id, err := GrokNativeIdentity(c)
		if err != nil {
			t.Fatal(err)
		}
		c.Identity = id
		parsed, err := credentialMetadata(c.Credential)
		if err != nil || parsed.Label != tc.want {
			t.Fatal("incorrect Grok identity label")
		}
	}
}

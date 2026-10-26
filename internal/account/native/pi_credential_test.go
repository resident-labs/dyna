package native

import (
	"fmt"
	"testing"
	"time"
)

// A grant's expiry is what the one ChatGPT reader compares with its renewal
// window, and a login without an expiry is never due.
func TestPiGrantExpiryReadsTheAccessTokenExpiry(t *testing.T) {
	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	got, err := PiGrantExpiry([]byte(fmt.Sprintf(`{"type":"oauth","access":"token","refresh":"refresh","expires":%d}`, expiry.UnixMilli())))
	if err != nil || !got.Equal(expiry) {
		t.Fatalf("expiry = %v, %v; want %v", got, err, expiry)
	}
	if got, err = PiGrantExpiry([]byte(`{"type":"oauth","access":"token","refresh":"refresh"}`)); err != nil || !got.IsZero() {
		t.Fatalf("without an expiry: expiry = %v, %v; want none", got, err)
	}
	if _, err = PiGrantExpiry([]byte(`{"type":"api_key","key":"k"}`)); err == nil {
		t.Fatal("an API key entry has no grant expiry, want a schema error")
	}
}

// A Muse OAuth sign-in keeps its minted inference key in access, and a pasted
// key keeps it in key: both shapes are the key its search endpoint takes.
func TestPiOAuthOrAPIKeyReadsBothSignInShapes(t *testing.T) {
	for _, test := range []struct {
		name    string
		entry   string
		want    string
		wantErr bool
	}{
		{name: "oauth access", entry: `{"type":"oauth","access":"minted-key","refresh":"identity"}`, want: "minted-key"},
		{name: "pasted key", entry: `{"type":"api_key","key":"pasted-key"}`, want: "pasted-key"},
		{name: "oauth without access", entry: `{"type":"oauth","refresh":"identity"}`, wantErr: true},
		{name: "api key without key", entry: `{"type":"api_key"}`, wantErr: true},
		{name: "unknown shape", entry: `{"type":"other","key":"pasted-key"}`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := PiOAuthOrAPIKey([]byte(test.entry))
			if test.wantErr {
				if err == nil {
					t.Fatalf("key = %q, want a schema error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("key = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

package native

import (
	"testing"
)

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

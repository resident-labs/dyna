package room

import "testing"

// A room names its fork's parent in any common form, and the setting stores it as host/owner/repo.
func TestParseUpstreamStoresTheParentAsHostOwnerRepo(t *testing.T) {
	for value, want := range map[string]string{
		"github.com/Horizon-Rust/Horizon":             "github.com/horizon-rust/horizon",
		"https://github.com/horizon-rust/horizon.git": "github.com/horizon-rust/horizon",
		"git@github.com:horizon-rust/horizon.git":     "github.com/horizon-rust/horizon",
		"off": "",
	} {
		got, err := ParseUpstream(value)
		if err != nil || got != want {
			t.Fatalf("ParseUpstream(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
	if _, err := ParseUpstream("github.com/horizon-rust"); err == nil {
		t.Fatal("a host and owner without a repository were accepted")
	}
}

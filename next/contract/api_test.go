package contract

import "testing"

func TestAgyExitHostIsGoogleOnly(t *testing.T) {
	for host, want := range map[string]string{
		"accounts.google.com": "accounts.google.com", "ACCOUNTS.Google.com.": "accounts.google.com", "google.com": "google.com",
		"daily-cloudcode-pa.googleapis.com": "daily-cloudcode-pa.googleapis.com", "ssl.gstatic.com": "ssl.gstatic.com", "lh3.googleusercontent.com": "lh3.googleusercontent.com",
	} {
		if got, ok := AgyExitHost(host); !ok || got != want {
			t.Errorf("%s: %q %v", host, got, ok)
		}
	}
	for _, host := range []string{"example.com", "evilgoogle.com", "googleapis.com.evil.com", "", "142.250.1.1", "a..google.com", "a_b.google.com", "[::1]", "notgoogle.com"} {
		if got, ok := AgyExitHost(host); ok {
			t.Errorf("%q was taken as Google's: %q", host, got)
		}
	}
}

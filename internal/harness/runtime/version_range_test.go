package runtime

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Each harness declares the version range Dyna's verified recipes support.
// Install and the update recommendation must refuse versions outside it, so
// an incompatible release never becomes the default for new sessions.
func TestInstallRejectsVersionsOutsideSupportedRange(t *testing.T) {
	m := manager(t)
	for _, tc := range []struct{ harness, version, reason string }{
		{"pi", "1.2.9", "Dyna's Pi fork"},
		{"pi", "1.4.0", "Dyna's Pi fork"},
		{"pi", "1.4.0-dyna.2", "Dyna's Pi fork"},
	} {
		_, err := m.Install(context.Background(), tc.harness, tc.version)
		if err == nil || !strings.Contains(err.Error(), tc.reason) {
			t.Fatalf("%s %s: got %v, want an error naming %q", tc.harness, tc.version, err, tc.reason)
		}
	}
	for _, tc := range []struct{ harness, version string }{
		{"pi", piForkVersion},
		{"claude-code", "0.1.0"},
	} {
		if _, err := m.Install(context.Background(), tc.harness, tc.version); err != nil {
			t.Fatalf("%s %s: %v", tc.harness, tc.version, err)
		}
	}
}

func TestPiForkVersionSatisfiesUpstreamMinimum(t *testing.T) {
	if !versionAtLeast(piForkVersion, "1.4.0") {
		t.Fatal("Dyna prerelease sorts below its upstream base version")
	}
	if ok, reason := supportedVersionRange("pi", piForkVersion); !ok {
		t.Fatalf("supported Pi fork was rejected: %s", reason)
	}
	if ok, reason := supportedVersionRange("pi", "1.4.0"); ok {
		t.Fatalf("upstream npm Pi accepted: %s", reason)
	}
	if versionAtLeast("1.2.9-dyna.4", "1.3.0") {
		t.Fatal("Dyna suffix raised an older upstream base version")
	}
}

// The latest-version lookup respects the same ranges, so the Harnesses page
// never recommends a version Dyna refuses to install.
func TestRecommendedVersionsRespectSupportedRange(t *testing.T) {
	for _, tc := range []struct{ harness, pkg, version, reason string }{
		{"pi", piNPMPackage, "1.2.9", "fork"},
	} {
		r := RecommendedVersionResolver{HTTPClient: recommendedClientFunc(func(*http.Request) (*http.Response, error) {
			return recommendedResponse(200, recommendedMetadata(tc.pkg, tc.version)), nil
		})}
		a, _ := Adapter(tc.harness)
		v, reason := r.npmVersion(context.Background(), a, "https://registry.npmjs.org/example/latest")
		if v != "" || !strings.Contains(reason, tc.reason) {
			t.Fatalf("%s %s: got %q, %q; want a rejection naming %q", tc.harness, tc.version, v, reason, tc.reason)
		}
	}
}

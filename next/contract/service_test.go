package contract

import (
	"strings"
	"testing"
)

// A data path holding a backslash, a dollar sign or an equals sign could continue a quadlet line or expand, so it is refused.
func TestServiceDataRefusesCharactersThatBreakAQuadletLine(t *testing.T) {
	image := "ghcr.io/o/r@sha256:" + strings.Repeat("a", 64)
	for _, data := range []string{"/data\\", "/da$ta", "/a=b", "/a b", "/a:b"} {
		if _, err := (ServiceSpec{Name: "web", Image: image, Data: data}).Parse(); err == nil {
			t.Errorf("data %q accepted", data)
		}
	}
	if _, err := (ServiceSpec{Name: "web", Image: image, Data: "/var/lib/web"}).Parse(); err != nil {
		t.Fatal(err)
	}
}

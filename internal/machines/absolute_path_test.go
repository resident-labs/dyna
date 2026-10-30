package machines

import "testing"

func TestValidateAbsolutePath(t *testing.T) {
	for _, tc := range []struct {
		value string
		ok    bool
	}{
		{"/srv/data", true},
		{"/dev/dri/renderD128", true},
		{"", false},
		{"srv/data", false},
		{"/srv/../data", false},
		{"/srv/data/", false},
		{"/dev/bus:usb", false},
		{"/srv/data:ro", false},
		{"/srv/\x00data", false},
	} {
		if err := ValidateAbsolutePath(tc.value); (err == nil) != tc.ok {
			t.Fatalf("ValidateAbsolutePath(%q) error = %v; want ok %v", tc.value, err, tc.ok)
		}
	}
}

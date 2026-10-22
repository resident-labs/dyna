package settings

import (
	"testing"
	"time"
)

func TestParseBytes(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int64
	}{
		{"25MiB", 25 << 20}, {"25 MiB", 25 << 20}, {"25MB", 25 << 20}, {"26214400", 25 << 20}, {"26,214,401 bytes", 26214401}, {"25m", 25 << 20},
		{"512 KiB", 512 << 10}, {"1.5 GiB", 3 << 29}, {" 1 kb ", 1024}, {"1 byte", 1}, {"1.125 KiB", 1152}, {"9223372036854775807", 1<<63 - 1},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseBytes(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("got %d, %v; want %d", got, err, tc.want)
			}
		})
	}
	for _, input := range []string{"", "0", "0MiB", "-1", "-25 MiB", "garbage", "25 MiB junk", "1.1 bytes", "1.5", "1e6", "9223372036854775808", "999999999999 EiB", "NaN", "1 KB/s", "12,00 bytes", "1,,000", ",100"} {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseBytes(input); err == nil {
				t.Fatal("accepted invalid size")
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{{25 << 20, "25 MiB"}, {512 << 10, "512 KiB"}, {1610612736, "1.5 GiB"}, {1152, "1,152 bytes"}, {12000, "12,000 bytes"}, {1536, "1.5 KiB"}, {1280, "1.25 KiB"}, {1, "1 bytes"}, {0, "0 bytes"}, {26214401, "26,214,401 bytes"}, {MaxContentBytes, "562,949,953,421,311 bytes"}} {
		if got := FormatBytes(tc.n); got != tc.want {
			t.Errorf("FormatBytes(%d) = %q; want %q", tc.n, got, tc.want)
		}
		if tc.n > 0 {
			n, err := ParseBytes(FormatBytes(tc.n))
			if err != nil || n != tc.n {
				t.Errorf("round trip %d: %d %v", tc.n, n, err)
			}
		}
	}
}

func TestFormatDuration(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"2m0s", "2m"}, {"1h0m0s", "1h"}, {"1h2m0s", "1h2m"}, {"1m30s", "1m30s"}, {"500ms", "500ms"}, {"1h0m0.5s", "1h0.5s"}} {
		d, err := time.ParseDuration(tc.input)
		if err != nil {
			t.Fatal(err)
		}
		if got := FormatDuration(d); got != tc.want {
			t.Errorf("got %s; want %s", got, tc.want)
		}
	}
}

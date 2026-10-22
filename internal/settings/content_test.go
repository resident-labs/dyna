package settings

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestContentLimitsLiveAndValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Content()
	if err != nil || c.CardBytes != 25<<20 || c.FileBytes != 25<<20 || c.PostBytes() != 200<<20 {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	v, _ := Load(DefaultPath())
	for _, key := range []string{KeyMaxCardBytes, KeyMaxFileBytes} {
		for _, bad := range []string{"0", "-1", "1.5", "1wat", "NaN", "9223372036854775807"} {
			if v.Set(key, bad) == nil {
				t.Fatalf("accepted %s=%s", key, bad)
			}
		}
	}
	for _, n := range []int{7, 31, 4} {
		if err := v.Set(KeyMaxFileBytes, strconv.Itoa(n)); err != nil {
			t.Fatal(err)
		}
		if err := v.Set(KeyMaxCardBytes, "11"); err != nil {
			t.Fatal(err)
		}
		if err := v.Save(); err != nil {
			t.Fatal(err)
		}
		c, err = Content()
		if err != nil || c.FileBytes != int64(n) || c.CardBytes != 11 {
			t.Fatalf("live: %+v %v", c, err)
		}
	}
	// Eight separately padded base64 values plus JSON metadata fit the derived transport.
	for _, c := range []ContentLimits{{1, 1}, {11, 4}, {4, 11}, {MaxContentBytes, MaxContentBytes}} {
		if c.JSONBytes() <= c.PostBytes() || c.JSONBytes() > int64(^uint(0)>>1) {
			t.Fatalf("overflow %+v", c)
		}
		if c.FileBytes < 100 {
			data := make([]string, 8)
			for i := range data {
				n := c.FileBytes
				if i == 0 {
					n = max(n, c.CardBytes)
				}
				data[i] = base64.StdEncoding.EncodeToString(make([]byte, n))
			}
			b, _ := json.Marshal(data)
			if int64(len(b)) > c.JSONBytes() {
				t.Fatal("base64 overhead")
			}
		}
	}
	for _, bad := range []string{`0`, `1.5`, `"25MiB"`, `1e30`} {
		os.WriteFile(DefaultPath(), []byte(`{"content":{"maxCardBytes":`+bad+`}}`), 0600)
		if _, err := Content(); err == nil || !strings.Contains(err.Error(), KeyMaxCardBytes) {
			t.Fatalf("invalid stored setting %s: %v", bad, err)
		}
	}
}

func TestBoundedContentReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	os.WriteFile(path, []byte("12345"), 0600)
	if _, err := ReadContentFile(path, 4); err == nil || !strings.Contains(err.Error(), "maximum 4 bytes") {
		t.Fatalf("limit: %v", err)
	}
	if b, err := ReadContentFile(path, 5); err != nil || string(b) != "12345" {
		t.Fatalf("exact: %q %v", b, err)
	}
	if _, err := ReadContentFile(filepath.Dir(path), 5); err == nil {
		t.Fatal("accepted directory")
	}
	if b, err := ReadStoredFile(path); err != nil || len(b) != 5 {
		t.Fatalf("stored: %v", err)
	}
}

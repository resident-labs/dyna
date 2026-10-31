package model

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"
)

func TestLoadCatalogBounds(t *testing.T) {
	pack := func(body string) []byte {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: "package/dyna-catalog.json", Mode: 0600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	c, skipped, err := LoadCatalog(pack(`{"version":1,"providers":[{"id":"deepseek","models":[{"id":"deepseek-v4"}]},{"id":"plan_bad","models":[]}]}`))
	if err != nil || len(c.Providers) != 1 || c.Providers[0].ID != "deepseek" || len(skipped) != 1 || skipped[0] != "plan_bad: invalid provider ID or model count" {
		t.Fatalf("catalog=%+v skipped=%v err=%v", c, skipped, err)
	}
	_, skipped, err = LoadCatalog(pack(`{"version":1,"providers":[{"id":"bad","headers":{"X-Static":"$Box-Header"},"models":[]}]}`))
	if err != nil || len(skipped) != 1 || skipped[0] != "bad: a header value cannot start with $" {
		t.Fatalf("unsafe header: skipped=%v err=%v", skipped, err)
	}
	_, skipped, err = LoadCatalog(pack(`{"version":1,"providers":[{"id":"equal","models":[{"id":"equal"}]}]}`))
	if err != nil || len(skipped) != 1 || skipped[0] != "equal: invalid model ID" {
		t.Fatalf("prefix-equal ID: skipped=%v err=%v", skipped, err)
	}
	if _, _, err := LoadCatalog(pack(`{"version":2,"providers":[]}`)); err == nil {
		t.Fatal("accepted invalid catalog")
	}
	for _, raw := range []string{"http://localhost/v1", "https://user@example.com/v1", "https://example.com:8443/v1", "https://example.com/v1?key=x", "https://{host}/v1"} {
		if fixedCatalogURL(raw) {
			t.Errorf("accepted %q", raw)
		}
	}
	if !fixedCatalogURL("https://example.com/v1") {
		t.Fatal("rejected HTTPS URL")
	}
}

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
	c, err := LoadCatalog(pack(`{"version":1,"keys":[{"provider":"deepseek","models":[{"id":"deepseek-v4"}]}]}`))
	if err != nil || len(c.Keys) != 1 || c.Keys[0].Provider != "deepseek" {
		t.Fatalf("catalog=%+v err=%v", c, err)
	}
	if _, err := LoadCatalog(pack(`{"version":2}`)); err == nil {
		t.Fatal("accepted invalid catalog version")
	}
}

package notices

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDiskFactsJSONNilPaths(t *testing.T) {
	got, err := json.Marshal(DiskFacts{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(`{"paths":null}`)) {
		t.Fatalf("got %s; want nil paths and omitted optional fields", got)
	}
}

package runstore

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalizedOutputTailMatchesFullRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.jsonl")
	data := []byte("{\"kind\":\"message\",\"text\":\"one\"}\ninvalid\n{\"kind\":\"message\",\"text\":\"two\"}\n")
	data = append(data, bytes.Repeat([]byte{'x'}, normalizedOutputMaxRecordBytes+1)...)
	data = append(data, []byte("\n{\"kind\":\"message\",\"text\":\"three\"}\n{\"kind\":\"partial\"")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{0, int64(len(data) - 30)} {
		full, offset, err := ReadNormalizedOutputPath(path, 0, limit)
		if err != nil {
			t.Fatal(err)
		}
		for _, cap := range []int{1, 2, 3, 4} {
			got, next, err := ReadNormalizedOutputTailPath(path, 0, limit, cap)
			if err != nil {
				t.Fatal(err)
			}
			want := full
			if len(want) > cap {
				want = want[len(want)-cap:]
			}
			if next != offset || !reflect.DeepEqual(got, want) {
				t.Fatalf("cap %d, limit %d: tail or offset differs", cap, limit)
			}
		}
	}
}

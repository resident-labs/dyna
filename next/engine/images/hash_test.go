package images

import (
	"archive/tar"
	"bytes"
	"testing"
)

func contextTar(t *testing.T, files [][2]string) []byte {
	t.Helper()
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for _, file := range files {
		if err := w.WriteHeader(&tar.Header{Name: file[0], Mode: 0644, Size: int64(len(file[1]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(file[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestTagDependsOnInputsNotArchiveOrder(t *testing.T) {
	a := contextTar(t, [][2]string{{"Containerfile", "FROM base"}, {"deps", "a"}})
	b := contextTar(t, [][2]string{{"deps", "a"}, {"Containerfile", "FROM base"}})
	first, err := Tag("toolchain", "dyna-base:one", a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Tag("toolchain", "dyna-base:one", b)
	if err != nil || first != second {
		t.Fatalf("tags %q, %q, error %v", first, second, err)
	}
	changed, err := Tag("toolchain", "dyna-base:two", a)
	if err != nil || changed == first {
		t.Fatalf("base change did not change tag: %q", changed)
	}
}

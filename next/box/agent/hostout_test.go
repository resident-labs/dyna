package agent

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractHostOutKeepsFilesInsideTheRunFolder(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(h tar.Header, body string) {
		h.Size = int64(len(body))
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	add(tar.Header{Name: "./shot.png", Typeflag: tar.TypeReg, Mode: 0o644}, "png")
	add(tar.Header{Name: "./d/n.txt", Typeflag: tar.TypeReg, Mode: 0o644}, "n")
	add(tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o644}, "x")
	add(tar.Header{Name: "/abs", Typeflag: tar.TypeReg, Mode: 0o644}, "x")
	add(tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, "")
	tw.Close()
	root := t.TempDir()
	var out bytes.Buffer
	if err := extractHostOut(&buf, root, "op1", &out); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "op1")
	want := "\nFiles from $DYNA_OUT:\n" + filepath.Join(dir, "shot.png") + "\n" + filepath.Join(dir, "d", "n.txt") + "\n"
	if out.String() != want {
		t.Fatalf("out %q want %q", out.String(), want)
	}
	for _, p := range []string{filepath.Join(root, "escape"), filepath.Join(dir, "link"), filepath.Join(dir, "abs")} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s was written", p)
		}
	}
}

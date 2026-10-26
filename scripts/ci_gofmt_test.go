package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGofmtCheck(t *testing.T, files map[string]string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := exec.Command("git", "init", "-q")
	git.Dir = dir
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	add := exec.Command("git", "add", "-A")
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	script, err := filepath.Abs("ci-gofmt.sh")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", script, dir).CombinedOutput()
	return string(out), err
}

// TestGofmtCheckNamesUnformattedFiles pins that the go-static format phase
// fails on a tracked Go file in any module and names it.
func TestGofmtCheckNamesUnformattedFiles(t *testing.T) {
	out, err := runGofmtCheck(t, map[string]string{
		"ok.go":              "package a\n",
		"desktop/bad.go":     "package  b\n",
		"internal/x/also.go": "package c\nfunc f(){}\n",
	})
	if err == nil {
		t.Fatal("check passed with unformatted files")
	}
	for _, want := range []string{"desktop/bad.go", "internal/x/also.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not name %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ok.go") && !strings.Contains(out, "also.go") {
		t.Errorf("output names a formatted file:\n%s", out)
	}
}

func TestGofmtCheckPassesFormattedFiles(t *testing.T) {
	if out, err := runGofmtCheck(t, map[string]string{"a.go": "package a\n"}); err != nil {
		t.Fatalf("check failed: %v\n%s", err, out)
	}
}

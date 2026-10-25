package path

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func executable(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	// If discovery accidentally executes this file the test must fail.
	if err := os.WriteFile(path, []byte("not an executable format; discovery must only stat this file"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestPATHPrecedesNativeFallbacks(t *testing.T) {
	home := t.TempDir()
	first := t.TempDir()
	second := t.TempDir()
	want := executable(t, first, "opencode")
	executable(t, second, "opencode")
	executable(t, filepath.Join(home, ".opencode", "bin"), "opencode")
	got, err := Resolve("opencode", []string{"HOME=" + home, "PATH=" + first + string(os.PathListSeparator) + second})
	if err != nil || got != want || !filepath.IsAbs(got) {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestWindowsNativeResolutionUsesPATHEXTAndCaseInsensitiveEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.exe")
	if err := os.WriteFile(path, []byte("fixture; never execute"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveForOS("codex", []string{"Path=" + dir, "pathext=.EXE;.CMD", "HOME=" + t.TempDir()}, "windows")
	if err != nil || got != path {
		t.Fatalf("Windows executable lookup: %q %v", got, err)
	}
}
func TestOpenCodeNativeFallback(t *testing.T) {
	home := t.TempDir()
	want := executable(t, filepath.Join(home, ".opencode", "bin"), "opencode")
	got, err := Resolve("opencode", []string{"HOME=" + home, "PATH=" + t.TempDir()})
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}
func TestLocalBinFallbackForEveryNativeHarness(t *testing.T) {
	for _, binary := range []string{"claude", "codex", "kimi", "opencode", "muse"} {
		t.Run(binary, func(t *testing.T) {
			home := t.TempDir()
			want := executable(t, filepath.Join(home, ".local", "bin"), binary)
			got, err := Resolve(binary, []string{"HOME=" + home, "PATH=" + t.TempDir()})
			if err != nil || got != want {
				t.Fatalf("got %q, %v; want %q", got, err, want)
			}
		})
	}
}
func TestEffectiveEnvironmentOverridesHostAndEarlierPATH(t *testing.T) {
	host := t.TempDir()
	override := t.TempDir()
	home := t.TempDir()
	executable(t, host, "codex")
	want := executable(t, override, "codex")
	t.Setenv("PATH", host)
	got, err := Resolve("codex", []string{"HOME=" + home, "PATH=" + host, "PATH=" + override})
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}
func TestRejectsNonExecutableDirectoryBrokenLinkAndCustomNames(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("not executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "absent"), filepath.Join(dir, "muse")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "claude", "muse"} {
		if path, err := Resolve(name, []string{"HOME=" + home, "PATH=" + dir}); path != "" || !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("%s accepted: %q, %v", name, path, err)
		}
	}
	if _, err := Resolve("../custom", nil); err == nil {
		t.Fatal("custom executable accepted")
	}
}
func TestExecutableSymlinkIsAccepted(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	target := executable(t, t.TempDir(), "codex")
	want := filepath.Join(dir, "codex")
	if err := os.Symlink(target, want); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve("codex", []string{"HOME=" + home, "PATH=" + dir})
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestExplicitEmptyHomeDoesNotUseHostFallback(t *testing.T) {
	home := t.TempDir()
	executable(t, filepath.Join(home, ".local", "bin"), "codex")
	t.Setenv("HOME", home)
	if got, err := Resolve("codex", []string{"HOME=", "PATH=" + t.TempDir()}); got != "" || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("host home leaked into effective environment: %q, %v", got, err)
	}
}

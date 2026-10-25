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
	want := executable(t, first, "pi")
	executable(t, second, "pi")
	executable(t, filepath.Join(home, ".local", "bin"), "pi")
	got, err := Resolve("pi", []string{"HOME=" + home, "PATH=" + first + string(os.PathListSeparator) + second})
	if err != nil || got != want || !filepath.IsAbs(got) {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestWindowsNativeResolutionUsesPATHEXTAndCaseInsensitiveEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pi.exe")
	if err := os.WriteFile(path, []byte("fixture; never execute"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveForOS("pi", []string{"Path=" + dir, "pathext=.EXE;.CMD", "HOME=" + t.TempDir()}, "windows")
	if err != nil || got != path {
		t.Fatalf("Windows executable lookup: %q %v", got, err)
	}
}
func TestLocalBinFallbackForEveryNativeHarness(t *testing.T) {
	for _, binary := range []string{"claude", "pi"} {
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
	executable(t, host, "pi")
	want := executable(t, override, "pi")
	t.Setenv("PATH", host)
	got, err := Resolve("pi", []string{"HOME=" + home, "PATH=" + host, "PATH=" + override})
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}
func TestRejectsNonExecutableDirectoryBrokenLinkAndCustomNames(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pi"), []byte("not executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "claude"), 0700); err != nil {
		t.Fatal(err)
	}
	links := t.TempDir()
	if err := os.Symlink(filepath.Join(links, "absent"), filepath.Join(links, "pi")); err != nil {
		t.Fatal(err)
	}
	path := dir + string(os.PathListSeparator) + links
	for _, name := range []string{"pi", "claude"} {
		if path, err := Resolve(name, []string{"HOME=" + home, "PATH=" + path}); path != "" || !errors.Is(err, exec.ErrNotFound) {
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
	target := executable(t, t.TempDir(), "pi")
	want := filepath.Join(dir, "pi")
	if err := os.Symlink(target, want); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve("pi", []string{"HOME=" + home, "PATH=" + dir})
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestExplicitEmptyHomeDoesNotUseHostFallback(t *testing.T) {
	home := t.TempDir()
	executable(t, filepath.Join(home, ".local", "bin"), "pi")
	t.Setenv("HOME", home)
	if got, err := Resolve("pi", []string{"HOME=", "PATH=" + t.TempDir()}); got != "" || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("host home leaked into effective environment: %q, %v", got, err)
	}
}

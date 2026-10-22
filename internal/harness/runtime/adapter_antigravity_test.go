package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestAntigravityAdapterPrivateFileScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a, err := Adapter("agy")
	if err != nil {
		t.Fatal(err)
	}
	if a.Harness != "antigravity" || a.Binary != "agy" || a.NPMPackage != "" || !a.Exclusive || a.ExclusiveReason == "" || a.Install == nil {
		t.Fatalf("wrong Antigravity contract: %+v", a)
	}
	// Native login and turns share the private token file.
	if a.AccountBlocked != "" || !reflect.DeepEqual(a.AuthFiles, []string{".gemini/antigravity-cli/antigravity-oauth-token"}) || a.ConfigFile != "" || a.SkillDir != "config/skills" {
		t.Fatal("adapter invented a private credential or config scope")
	}
	wantEnv := map[string]string{
		"AGY_CLI_DISABLE_AUTO_UPDATE": "true",
		"SSH_TTY":                     "dyna-private-file-store",
		"JETSKI_OAUTH_TOKEN":          "", "GEMINI_API_KEY": "", "AGY_ADC_AUTH": "",
		"AGY_LLM_GATEWAY_API_KEY": "", "ANTIGRAVITY_GEMINI_DIR": "", "JETSKI_APP_DATA_DIR": "",
	}
	if got := a.NativeEnv(home); !reflect.DeepEqual(got, wantEnv) {
		t.Fatalf("wrong native environment: %v", got)
	}
	if got := a.ConfigArgs(home, filepath.Join(home, "mcp.json"), filepath.Join(home, "skills")); !reflect.DeepEqual(got, []string{"--add-dir", home}) {
		t.Fatalf("wrong generation workspace flags: %v", got)
	}
	exe := filepath.Join(home, "bundle with spaces", "agy")
	if got := a.LoginArgs(exe, home); !reflect.DeepEqual(got, []string{exe}) {
		t.Fatalf("native login argv: %v", got)
	}
	if len(a.ConfigAllowlist) != 0 || len(a.PersonalConfigFiles) != 0 {
		t.Fatal("adapter allows unverified preference import")
	}
	reasons := strings.Join(a.PartialReasons, "\n")

	if strings.Contains(reasons, "Secret Service") {
		t.Fatal("stale shared-login restriction")
	}
	if os.Getenv("HOME") != home {
		t.Fatal("adapter changed HOME")
	}
}

const testAntigravityURL = antigravityArtifactPrefix + "1.4.2-4835981233356800/linux-x64/cli_linux_x64.tar.gz"

var testAntigravitySHA512 = strings.Repeat("ab", 64)

func TestAntigravityInstallFollowsOfficialManifest(t *testing.T) {
	manifestURL, err := antigravityManifestURL(runtime.GOOS, runtime.GOARCH, muslLoader())
	if err != nil {
		t.Skip(err)
	}
	data, digest := antigravityTestArchive(t, &tar.Header{Name: "antigravity", Typeflag: tar.TypeReg, Mode: 0755, Size: 7})
	for _, tc := range []struct {
		name, version, url string
		ok                 bool
	}{
		{"latest", "1.4.2", testAntigravityURL, true},
		{"older version", "1.4.1", testAntigravityURL, false},
		{"foreign artifact", "1.4.2", "https://example.invalid/cli_linux_x64.tar.gz", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			staging := t.TempDir()
			t.Setenv("HOME", staging)
			manifest := `{"version":"1.4.2","url":"` + tc.url + `","sha512":"` + digest + `"}`
			downloads := []string{}
			m := &BundleManager{Run: func(_ context.Context, args, _ []string) error {
				source := args[len(args)-1]
				downloads = append(downloads, source)
				body := data
				if source == manifestURL {
					body = []byte(manifest)
				}
				return os.WriteFile(args[len(args)-2], body, 0600)
			}}
			got, err := installAntigravity(context.Background(), m, staging, tc.version, nil)
			if (err == nil) != tc.ok {
				t.Fatalf("got %+v, %v", got, err)
			}
			if tc.ok && (got.Package != testAntigravityURL || !reflect.DeepEqual(downloads, []string{manifestURL, testAntigravityURL})) {
				t.Fatalf("got %+v after %v", got, downloads)
			}
			if !tc.ok && len(downloads) != 1 {
				t.Fatalf("downloaded an unverified artifact: %v", downloads)
			}
		})
	}
}

func antigravityTestArchive(t *testing.T, headers ...*tar.Header) ([]byte, string) {
	t.Helper()
	var data bytes.Buffer
	compressed := gzip.NewWriter(&data)
	archive := tar.NewWriter(compressed)
	for _, h := range headers {
		if err := archive.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := archive.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum512(data.Bytes())
	return data.Bytes(), hex.EncodeToString(sum[:])
}

func TestAntigravityPinnedArchiveInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	staging := filepath.Join(home, "stage ; $(do-not-run)")
	if err := os.Mkdir(staging, 0700); err != nil {
		t.Fatal(err)
	}
	data, digest := antigravityTestArchive(t,
		&tar.Header{Name: "../outside", Typeflag: tar.TypeReg, Mode: 0700, Size: 2},
		&tar.Header{Name: "antigravity", Typeflag: tar.TypeReg, Mode: 0755, Size: 7},
	)
	env := []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "existing-xdg")}
	called := 0
	m := &BundleManager{Run: func(_ context.Context, args, gotEnv []string) error {
		called++
		if !reflect.DeepEqual(gotEnv, env) {
			t.Fatal("installer changed HOME or XDG")
		}
		if len(args) != 12 || !reflect.DeepEqual(args[:10], []string{"curl", "--fail", "--silent", "--show-error", "--location", "--proto", "=https", "--proto-redir", "=https", "--output"}) || args[11] != testAntigravityURL || !inside(staging, args[10]) {
			t.Fatalf("unexpected download argv: %v", args)
		}
		return os.WriteFile(args[10], data, 0600)
	}}
	got, err := installAntigravityArchive(context.Background(), m, staging, env, testAntigravityURL, digest)
	if err != nil {
		t.Fatal(err)
	}
	wantSum, _ := hex.DecodeString(digest)
	if called != 1 || got.Executable != filepath.Join("bin", "agy") || got.Package != testAntigravityURL || got.Integrity != "sha512-"+base64.StdEncoding.EncodeToString(wantSum) {
		t.Fatalf("wrong install result: %+v", got)
	}
	info, err := os.Stat(filepath.Join(staging, got.Executable))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0700 || info.Size() != 7 {
		t.Fatalf("executable not installed privately: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(home, "outside")); !os.IsNotExist(err) {
		t.Fatal("archive wrote outside staging")
	}
	entries, err := os.ReadDir(staging)
	if err != nil || len(entries) != 1 || entries[0].Name() != "bin" {
		t.Fatal("download scratch was not removed")
	}
}

func TestAntigravityArchiveRejectsInvalidPayloads(t *testing.T) {
	regular := &tar.Header{Name: "antigravity", Typeflag: tar.TypeReg, Mode: 0755, Size: 1}
	for _, tc := range []struct {
		name    string
		headers []*tar.Header
		corrupt bool
	}{
		{"checksum", []*tar.Header{regular}, true},
		{"symlink", []*tar.Header{{Name: "antigravity", Typeflag: tar.TypeSymlink, Linkname: "/owner/agy"}}, false},
		{"hardlink", []*tar.Header{{Name: "antigravity", Typeflag: tar.TypeLink, Linkname: "../owner/agy"}}, false},
		{"missing", []*tar.Header{{Name: "bin/antigravity", Typeflag: tar.TypeReg, Size: 1}}, false},
		{"duplicate", []*tar.Header{regular, regular}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			staging := t.TempDir()
			t.Setenv("HOME", staging)
			data, digest := antigravityTestArchive(t, tc.headers...)
			if tc.corrupt {
				digest = testAntigravitySHA512
			}
			m := &BundleManager{Run: func(_ context.Context, args, _ []string) error { return os.WriteFile(args[10], data, 0600) }}
			if _, err := installAntigravityArchive(context.Background(), m, staging, nil, testAntigravityURL, digest); err == nil {
				t.Fatal("accepted invalid archive")
			}
			if tc.corrupt {
				if _, err := os.Stat(filepath.Join(staging, "bin")); !os.IsNotExist(err) {
					t.Fatal("extracted before verifying checksum")
				}
			}
		})
	}
}

func TestAntigravityInstallFailsClosed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	called := false
	m := &BundleManager{Run: func(context.Context, []string, []string) error { called = true; return errors.New("fake failure") }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := installAntigravityArchive(ctx, m, home, nil, testAntigravityURL, testAntigravitySHA512); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored canceled install")
	}
	if called {
		t.Fatal("ran a command for an invalid recipe")
	}
	if _, err := installAntigravityArchive(context.Background(), m, home, nil, testAntigravityURL, testAntigravitySHA512); err == nil || !called {
		t.Fatal("download failure was not returned")
	}
	link := filepath.Join(home, "linked-stage")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	called = false
	if _, err := installAntigravityArchive(context.Background(), m, link, nil, testAntigravityURL, testAntigravitySHA512); err == nil || called {
		t.Fatal("followed staging symlink")
	}
}

func TestAntigravityOfficialPlatformManifests(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		musl         bool
		manifest     string
	}{
		{"linux", "amd64", false, "linux_amd64"},
		{"linux", "arm64", false, "linux_arm64"},
		{"linux", "amd64", true, "linux_amd64_musl"},
		{"linux", "arm64", true, "linux_arm64_musl"},
		{"darwin", "amd64", false, "darwin_amd64"},
		{"darwin", "arm64", false, "darwin_arm64"},
		{"darwin", "arm64", true, "darwin_arm64"},
		{"windows", "amd64", false, ""},
		{"android", "arm64", false, ""},
		{"linux", "386", false, ""},
		{"darwin", "386", false, ""},
	} {
		name := tc.goos + "_" + tc.goarch
		if tc.musl {
			name += "_musl"
		}
		t.Run(name, func(t *testing.T) {
			got, err := antigravityManifestURL(tc.goos, tc.goarch, tc.musl)
			if tc.manifest == "" {
				if err == nil || got != "" {
					t.Fatal("unsupported platform selected an artifact", got, err)
				}
				return
			}
			want := "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/" + tc.manifest + ".json"
			if err != nil || got != want {
				t.Fatal("wrong official manifest", got, err)
			}
		})
	}
	a, err := Adapter("antigravity")
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range a.PartialReasons {
		if reason != "The official manifest offers only the latest version." && !strings.HasPrefix(reason, "No switch verified for Antigravity ") {
			t.Fatal("stale platform limitation", a.PartialReasons)
		}
	}
}

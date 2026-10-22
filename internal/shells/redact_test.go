package shells

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

// The command gets the real secret; output.log, which every viewer reads,
// shows only the mask.
func TestSuperviseMasksSecretEnvironmentValuesInOutput(t *testing.T) {
	const secret = "gho_testSecretValue1234567890"
	t.Setenv("DYNA_TEST_GH_TOKEN", secret)
	t.Setenv("DYNA_TEST_NOTE", "plain-visible-value")
	store := &Store{Root: t.TempDir()}
	meta, err := store.Create(StartOptions{
		Name: "env-dump",
		Cmd:  []string{"/bin/sh", "-c", `[ "$DYNA_TEST_GH_TOKEN" = "` + secret + `" ] && echo real-value-received; env | grep DYNA_TEST_; printf 'tail:%s' "$DYNA_TEST_GH_TOKEN"`},
		Cwd:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Supervise(context.Background(), meta.ID); err != nil {
		t.Fatal(err)
	}
	path, err := store.OutputPath(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if strings.Contains(out, secret) {
		t.Fatalf("output.log shows the secret:\n%s", out)
	}
	for _, want := range []string{"real-value-received", "DYNA_TEST_GH_TOKEN=" + Redacted, "tail:" + Redacted, "DYNA_TEST_NOTE=plain-visible-value"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output.log lacks %q:\n%s", want, out)
		}
	}
}

// A key that is not in the environment, such as one printed from a file, is
// found by gitleaks' default rules.
func TestRedactorMasksKeysOutsideTheEnvironment(t *testing.T) {
	key := "ghp_" + strings.Repeat("aB3dE6gH9k", 3) + "zYxWvU"
	got := newRedactor(nil).line("token in file: " + key + "\n")
	if strings.Contains(got, key) || !strings.Contains(got, Redacted) {
		t.Fatalf("redacted line = %q", got)
	}
	if plain := "build ok in 1.2 s\n"; newRedactor(nil).line(plain) != plain {
		t.Fatal("plain output changed")
	}
}

func TestSecretValuesPicksCredentialNamesOnly(t *testing.T) {
	got, _ := secretValues([]string{"GH_TOKEN=abcdefgh12", "OPENAI_API_KEY=sk-0123456789", "AWS_SECRET_ACCESS_KEY=abcdefgh12xyz", "PIN_TOKEN=1234", "HOME=/home/someone", "PATH=/usr/bin:/bin"})
	// Longest first, so "abcdefgh12xyz" is masked whole, not as a prefix.
	if len(got) != 3 || got[2] != "abcdefgh12" || !slices.Contains(got, "abcdefgh12xyz") || !slices.Contains(got, "sk-0123456789") {
		t.Fatalf("secretValues = %q", got)
	}
}

// testPEM is a fake key: real-looking delimiters around base64-like lines.
const testPEM = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtz\nc2gtZWQyNTUxOQAAACBrZXlCb2R5TGluZVR3b0Zha2VGb3JUZXN0c09ubHkAAAA\n-----END OPENSSH PRIVATE KEY-----"

func supervisedOutput(t *testing.T, script string) string {
	t.Helper()
	store := &Store{Root: t.TempDir()}
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/key.pem", []byte(testPEM+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := store.Create(StartOptions{Name: "keys", Cmd: []string{"/bin/sh", "-c", script}, Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Supervise(context.Background(), meta.ID); err != nil {
		t.Fatal(err)
	}
	path, err := store.OutputPath(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func checkNoKeyMaterial(t *testing.T, script string) {
	t.Helper()
	out := supervisedOutput(t, script)
	for _, line := range strings.Split(testPEM, "\n")[1:3] {
		if strings.Contains(out, line) || strings.Contains(out, line[:20]) {
			t.Fatalf("output.log shows key material:\n%s", out)
		}
	}
	if !strings.Contains(out, Redacted) {
		t.Fatalf("nothing masked:\n%s", out)
	}
}

// Output is masked line by line, so a multiline key in the environment must
// not slip through, whether env prints it or printf leaves off the newline.
func TestSuperviseMasksMultilineEnvironmentKey(t *testing.T) {
	t.Setenv("DYNA_TEST_PRIVATE_KEY", testPEM)
	checkNoKeyMaterial(t, `env | grep -A3 DYNA_TEST_PRIVATE_KEY; printf '%s' "$DYNA_TEST_PRIVATE_KEY"`)
}

// A key printed from a file is masked by PEM state alone: it is not in the
// environment. Cases: cat, BEGIN joined to body and cut off, body joined to
// END with no BEGIN shown, and no final newline.
func TestSuperviseMasksFileKey(t *testing.T) {
	// A different key in the environment with the same delimiters must
	// not hide this file key's BEGIN line.
	other := strings.Join([]string{strings.Split(testPEM, "\n")[0], "ZW52aXJvbm1lbnRLZXlCb2R5T25seUZvclRlc3RzMDEyMzQ1Njc4OWFiY2RlZg", strings.Split(testPEM, "\n")[3]}, "\n")
	t.Setenv("DYNA_TEST_OTHER_PRIVATE_KEY", other)
	// cat; BEGIN joined to the first body line, cut off; the last body
	// line joined to END, with no BEGIN shown; no final newline.
	checkNoKeyMaterial(t, `cat key.pem; head -n 2 key.pem | tr -d '\n'; echo; tail -n 2 key.pem | tr -d '\n'; echo; head -c -1 key.pem`)
}

// A multiline secret's short last line, such as a PEM key's final base64
// line, is masked too: alone, with text after it, and on a line that also
// holds another secret, which must stay masked.
func TestSuperviseMasksShortTailOfMultilineSecret(t *testing.T) {
	t.Setenv("DYNA_TEST_API_KEY", "abcdefgh12\nijkl")
	t.Setenv("DYNA_TEST_OTHER_TOKEN", "other-secret-value-42")
	// A longer secret that starts with the short line must be masked whole.
	t.Setenv("DYNA_TEST_OVERLAP_TOKEN", "ijklmnopqrstuvwx")
	// A short line that starts a key gitleaks finds in a file.
	t.Setenv("DYNA_TEST_GH_API_KEY", "abcdefgh34\nghp_")
	pat := "ghp_" + strings.Repeat("aB3dE6gH9k", 3) + "zYxWvU"
	out := supervisedOutput(t, `printf '%s\n' "$DYNA_TEST_API_KEY"; printf '%s suffix\n' "$DYNA_TEST_API_KEY"; env | grep -A1 DYNA_TEST_API_KEY; printf '%s=%s\n' "$DYNA_TEST_OTHER_TOKEN" "$DYNA_TEST_API_KEY" | head -n 1; printf '%s\n' "$DYNA_TEST_API_KEY" | tail -n 1; printf 'abcdefgh12\n%s\n' "$DYNA_TEST_OVERLAP_TOKEN"; printf 'abcdefgh12\nprefix:ijkl suffix\n'; printf 'abcdefgh34\n`+pat+`\n'`)
	for _, leak := range []string{"abcdefgh12", "other-secret-value-42", "mnopqrstuvwx", "ijkl", pat[4:]} {
		if strings.Contains(out, leak) {
			t.Fatalf("output.log shows %q:\n%s", leak, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ijkl") {
			t.Fatalf("output.log shows the short secret line:\n%s", out)
		}
	}
	if !strings.Contains(out, Redacted+" suffix") {
		t.Fatalf("text after the secret was lost:\n%s", out)
	}
}

// gitleaks:allow silences a source scanner; it must not unmask shell output.
func TestRedactorIgnoresGitleaksAllow(t *testing.T) {
	key := "ghp_" + strings.Repeat("aB3dE6gH9k", 3) + "zYxWvU"
	got := newRedactor(nil).line(`token = "` + key + `" # gitleaks:allow` + "\n")
	if strings.Contains(got, key) {
		t.Fatalf("redacted line = %q", got)
	}
}

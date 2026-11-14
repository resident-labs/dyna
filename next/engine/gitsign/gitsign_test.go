package gitsign

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

const (
	id      = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	ident   = "Aria Owner <aria@example.com> 1700000000 +0000"
	other   = "Mallory <m@example.com> 1700000000 +0000"
	commit  = "tree " + id + "\nauthor " + ident + "\ncommitter " + ident + "\n\nmessage\n"
	tagText = "object " + id + "\ntype commit\ntag v1\ntagger " + ident + "\n\nrelease\n"
)

func TestCheckAcceptsCommitsAndTagsOfTheIdentity(t *testing.T) {
	for name, payload := range map[string]string{
		"commit":            commit,
		"tag":               tagText,
		"another author":    "tree " + id + "\nauthor " + other + "\ncommitter " + ident + "\n\nm\n",
		"merge":             "tree " + id + "\nparent " + id + "\nparent " + id + "\nauthor " + ident + "\ncommitter " + ident + "\n\nm\n",
		"no message":        "tree " + id + "\nauthor " + ident + "\ncommitter " + ident + "\n\n",
		"sha256 objects":    "tree " + id + strings.Repeat("0", 24) + "\nauthor " + ident + "\ncommitter " + ident + "\n\nm\n",
		"another timezone":  "tree " + id + "\nauthor " + ident + "\ncommitter Aria Owner <aria@example.com> 5 -0700\n\nm\n",
		"committer in body": commit + "committer " + other + "\n",
	} {
		if err := Check([]byte(payload), "Aria Owner", "aria@example.com"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// git trims "crud" from both ends of a configured name when it writes the line.
	if err := Check([]byte(commit), ".Aria Owner.", "aria@example.com"); err != nil {
		t.Errorf("a name git trims: %v", err)
	}
}

func TestCheckRefuses(t *testing.T) {
	cases := map[string]struct{ payload, name, email, say string }{
		"other committer":     {"tree " + id + "\nauthor " + ident + "\ncommitter " + other + "\n\nm\n", "Aria Owner", "aria@example.com", "user.name"},
		"other tagger":        {"object " + id + "\ntype commit\ntag v1\ntagger " + other + "\n\nm\n", "Aria Owner", "aria@example.com", "tagger"},
		"email differs":       {commit, "Aria Owner", "other@example.com", "other@example.com"},
		"name is a suffix":    {"tree " + id + "\nauthor x <x> 1 +0000\ncommitter Evil Aria Owner <aria@example.com> 1 +0000\n\nm\n", "Aria Owner", "aria@example.com", "committer"},
		"junk after the time": {"tree " + id + "\nauthor x <x> 1 +0000\ncommitter " + ident + " x\n\nm\n", "Aria Owner", "aria@example.com", "committer"},
		"two committers":      {"tree " + id + "\nauthor x <x> 1 +0000\ncommitter " + ident + "\ncommitter " + ident + "\n\nm\n", "Aria Owner", "aria@example.com", "one committer"},
		"no committer":        {"tree " + id + "\nauthor " + ident + "\n\nm\n", "Aria Owner", "aria@example.com", "one committer"},
		"tag without tagger":  {"object " + id + "\ntype commit\ntag v1\n\nm\n", "Aria Owner", "aria@example.com", "one tagger"},
		"a continuation line": {"tree " + id + "\nauthor x <x> 1 +0000\ngpgsig a\n committer " + ident + "\n\nm\n", "Aria Owner", "aria@example.com", "one committer"},
		"a file":              {"just a file\n", "Aria Owner", "aria@example.com", "commit and tag"},
		"empty":               {"", "Aria Owner", "aria@example.com", "commit and tag"},
		"bad tree id":         {"tree xyz\ncommitter " + ident + "\n\nm\n", "Aria Owner", "aria@example.com", "commit and tag"},
		"no identity name":    {commit, "", "aria@example.com", "no git identity"},
		"no identity email":   {commit, "Aria Owner", "", "no git identity"},
	}
	for label, tc := range cases {
		if err := Check([]byte(tc.payload), tc.name, tc.email); err == nil || !strings.Contains(err.Error(), tc.say) {
			t.Errorf("%s: got %v, want a refusal with %q", label, err, tc.say)
		}
	}
}

func TestOpenMakesOneKeyWithMode0600AndKeepsIt(t *testing.T) {
	dir := t.TempDir()
	key, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, KeyFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key file: %v %v", info, err)
	}
	again, err := Open(dir)
	if err != nil || again.PublicKey() != key.PublicKey() {
		t.Fatalf("reopened key differs: %v", err)
	}
	if !strings.HasPrefix(key.PublicKey(), "ssh-ed25519 ") || strings.Contains(key.PublicKey(), "\n") {
		t.Fatalf("public key line: %q", key.PublicKey())
	}
	if strings.Contains(key.PublicKey(), "PRIVATE") {
		t.Fatal("the public line holds private text")
	}
}

// The SSHSIG envelope is read back by hand, and the signature is checked with the public key against the signed blob that
// PROTOCOL.sshsig defines. TestSignatureVerifiesWithSSHKeygen below checks it against OpenSSH itself.
func TestSignIsAnArmoredSSHSIG(t *testing.T) {
	key, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	armored, err := key.Sign([]byte(commit))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(armored, "\n"), "\n")
	if lines[0] != "-----BEGIN SSH SIGNATURE-----" || lines[len(lines)-1] != "-----END SSH SIGNATURE-----" {
		t.Fatalf("armor: %q", armored)
	}
	for _, line := range lines[1 : len(lines)-1] {
		if len(line) > 70 {
			t.Fatalf("line of %d characters", len(line))
		}
	}
	blob, err := base64.StdEncoding.DecodeString(strings.Join(lines[1:len(lines)-1], ""))
	if err != nil {
		t.Fatal(err)
	}
	if string(blob[:6]) != "SSHSIG" || string(blob[6:10]) != "\x00\x00\x00\x01" {
		t.Fatalf("magic and version: %q", blob[:10])
	}
	var envelope struct {
		Key, Namespace, Reserved, Hash string
		Signature                      []byte
	}
	if err := ssh.Unmarshal(blob[10:], &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Namespace != "git" || envelope.Reserved != "" || envelope.Hash != "sha512" {
		t.Fatalf("envelope: %+v", envelope)
	}
	public, err := ssh.ParsePublicKey([]byte(envelope.Key))
	if err != nil || string(ssh.MarshalAuthorizedKey(public)) != key.PublicKey()+"\n" {
		t.Fatalf("public key in envelope: %v", err)
	}
	var signature ssh.Signature
	if err := ssh.Unmarshal(envelope.Signature, &signature); err != nil || signature.Format != "ssh-ed25519" {
		t.Fatalf("signature: %+v %v", signature, err)
	}
	hash := sha512Sum([]byte(commit))
	if err := public.Verify(sshsigData(hash), &signature); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	if err := public.Verify(sshsigData(sha512Sum([]byte(commit+"x"))), &signature); err == nil {
		t.Fatal("signature verifies another message")
	}
}

func TestSignatureVerifiesWithSSHKeygen(t *testing.T) {
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen is not installed")
	}
	key, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	armored, err := key.Sign([]byte(commit))
	if err != nil {
		t.Fatal(err)
	}
	sig := filepath.Join(dir, "sig")
	allowed := filepath.Join(dir, "allowed")
	if err := os.WriteFile(sig, []byte(armored), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allowed, []byte("aria@example.com "+key.PublicKey()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	verify := func(namespace, message string) error {
		cmd := exec.Command(keygen, "-Y", "verify", "-f", allowed, "-I", "aria@example.com", "-n", namespace, "-s", sig)
		cmd.Stdin = strings.NewReader(message)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("%s", out)
		}
		return err
	}
	if err := verify("git", commit); err != nil {
		t.Fatalf("ssh-keygen refuses the signature: %v", err)
	}
	if verify("file", commit) == nil || verify("git", commit+"x") == nil {
		t.Fatal("ssh-keygen accepts another namespace or message")
	}
}

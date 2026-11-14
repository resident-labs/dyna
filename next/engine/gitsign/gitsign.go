// Package gitsign holds the engine's SSH signing key for the commits and tags that boxes make as the owner. The private
// half stays in a file under the engine's data directory and never leaves this package: callers get signatures and the
// public key line.
package gitsign

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Namespace is the only SSHSIG namespace the engine signs: git's.
const Namespace = "git"

// KeyFile is the private key's name under the data directory, next to store.db.
const KeyFile = "git-signing-key"

type Key struct {
	signer ssh.Signer
}

// Open loads the key in dir, or makes one with mode 0600 on first use.
func Open(dir string) (*Key, error) {
	path := filepath.Join(dir, KeyFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(private, "dyna git signing key")
		if err != nil {
			return nil, err
		}
		data = pem.EncodeToMemory(block)
		// O_EXCL: two openers cannot each keep a different key.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			return Open(dir)
		}
		if err != nil {
			return nil, err
		}
		_, err = f.Write(data)
		if closed := f.Close(); err == nil {
			err = closed
		}
		if err != nil {
			os.Remove(path)
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return &Key{signer: signer}, nil
}

// PublicKey is the authorized_keys line to paste on GitHub as a signing key.
func (k *Key) PublicKey() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k.signer.PublicKey())))
}

// Sign returns the armored SSHSIG of payload in the git namespace, as ssh-keygen -Y sign writes it.
func (k *Key) Sign(payload []byte) (string, error) {
	signed := sshsigData(sha512Sum(payload))
	signature, err := k.signer.Sign(rand.Reader, signed)
	if err != nil {
		return "", err
	}
	blob := []byte("SSHSIG")
	blob = append(blob, 0, 0, 0, 1) // version
	blob = appendString(blob, k.signer.PublicKey().Marshal())
	blob = appendString(blob, []byte(Namespace))
	blob = appendString(blob, nil) // reserved
	blob = appendString(blob, []byte("sha512"))
	blob = appendString(blob, ssh.Marshal(struct {
		Format string
		Blob   []byte
	}{signature.Format, signature.Blob}))
	encoded := base64.StdEncoding.EncodeToString(blob)
	var out strings.Builder
	out.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(encoded) > 70 {
		out.WriteString(encoded[:70] + "\n")
		encoded = encoded[70:]
	}
	out.WriteString(encoded + "\n-----END SSH SIGNATURE-----\n")
	return out.String(), nil
}

// sshsigData is the blob the key signs: the magic, then the namespace, the reserved string, the hash algorithm and the
// hash of the message.
func sshsigData(hash []byte) []byte {
	data := []byte("SSHSIG")
	data = appendString(data, []byte(Namespace))
	data = appendString(data, nil)
	data = appendString(data, []byte("sha512"))
	return appendString(data, hash)
}

func appendString(b, s []byte) []byte {
	b = append(b, byte(len(s)>>24), byte(len(s)>>16), byte(len(s)>>8), byte(len(s)))
	return append(b, s...)
}

var (
	objectID  = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	timestamp = regexp.MustCompile(`^[0-9]+ [+-][0-9]{4}$`)
)

// Check accepts a payload only when it is a git commit or tag object whose committer or tagger is name <email>. The
// payload is what git hands a signer: the object without its signature.
func Check(payload []byte, name, email string) error {
	if name == "" || email == "" {
		return errors.New("the engine has no git identity configured for this box, so it signs nothing")
	}
	head, _, _ := bytes.Cut(payload, []byte("\n\n"))
	var first, role string
	var idents []string
	for i, line := range strings.Split(string(head), "\n") {
		key, value, _ := strings.Cut(line, " ")
		if i == 0 {
			first = key
			if !objectID.MatchString(value) || key != "tree" && key != "object" {
				return errors.New("the engine signs only git commit and tag objects")
			}
			role = map[string]string{"tree": "committer", "object": "tagger"}[key]
		}
		if key == role && !strings.HasPrefix(line, " ") {
			idents = append(idents, value)
		}
	}
	if len(idents) != 1 {
		return fmt.Errorf("the engine signs only git commit and tag objects with one %s line (this one has %d)", role, len(idents))
	}
	// git trims "crud" from both ends of a name when it writes the line.
	want := strings.Trim(name, " .,:;<>\"\\'") + " <" + email + "> "
	rest, ok := strings.CutPrefix(idents[0], want)
	if !ok || !timestamp.MatchString(rest) {
		who := "committer"
		if first == "object" {
			who = "tagger"
		}
		return fmt.Errorf("the engine signs only as %s <%s> and this %s is %q. Set git config user.name and user.email to the identity Dyna configured for this box, and unset GIT_COMMITTER_NAME and GIT_COMMITTER_EMAIL", name, email, who, idents[0])
	}
	return nil
}

func sha512Sum(b []byte) []byte {
	sum := sha512.Sum512(b)
	return sum[:]
}

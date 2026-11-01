package connect

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
)

type Link struct {
	Relay   string `json:"relay"`
	Machine string `json:"machine"`
	Name    string `json:"name"`
	Owner   Owner  `json:"owner"`
	App     string `json:"app"`
	Cards   string `json:"cards"`
}

// Files holds Connect credentials and immutable link configuration.
type Files struct {
	dir string
	mu  sync.Mutex
}

func OpenFiles(data string) (*Files, error) {
	dir := box.Layout{Data: data}.Connect()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	return &Files{dir: dir}, nil
}

func (f *Files) write(name string, b []byte) error {
	tmp, err := os.CreateTemp(f.dir, ".connect-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(f.dir, name))
}

func (f *Files) existingKey() (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(filepath.Join(f.dir, "machine.key"))
	if err != nil {
		return nil, err
	}
	seed, err := b64.DecodeString(string(b))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("invalid machine key")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func (f *Files) Key() (ed25519.PrivateKey, error) {
	key, err := f.existingKey()
	if !errors.Is(err, os.ErrNotExist) {
		return key, err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err = rand.Read(seed); err != nil {
		return nil, err
	}
	if err = f.write("machine.key", []byte(b64.EncodeToString(seed))); err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// Link reads the configuration. Callers must also check durable linked state.
func (f *Files) Link() (Link, error) {
	var link Link
	b, err := os.ReadFile(filepath.Join(f.dir, "link.json"))
	if err == nil {
		err = json.Unmarshal(b, &link)
	}
	return link, err
}

func (f *Files) Keys(now time.Time) (KeySet, error) {
	var keys KeySet
	b, err := os.ReadFile(filepath.Join(f.dir, "jwks.json"))
	if err != nil {
		return keys, err
	}
	info, err := os.Stat(filepath.Join(f.dir, "jwks.json"))
	if err != nil || now.Sub(info.ModTime()) >= 7*24*time.Hour {
		return keys, errors.New("relay keys expired")
	}
	err = json.Unmarshal(b, &keys)
	return keys, err
}

func saveJSON[T Link | KeySet](f *Files, name string, value T) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return f.write(name, b)
}
func (f *Files) SaveLink(link Link) error           { return saveJSON(f, "link.json", link) }
func (f *Files) SaveKeys(keys KeySet) error         { return saveJSON(f, "jwks.json", keys) }
func (f *Files) SaveTunnelToken(token string) error { return f.write("tunnel-token", []byte(token)) }

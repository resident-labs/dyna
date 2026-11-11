// Package credential stores control-plane credentials outside boxes and SQLite.
package credential

import (
	"errors"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Files keeps credentials outside the database and replaces them atomically.
// Dir must be a private control-plane configuration directory, not a box mount.
type Files struct{ Dir string }

func (f *Files) path(room contract.RoomID) string {
	// Encoding the ID avoids interpreting even an invalid ID as a path.
	return filepath.Join(f.Dir, fmt.Sprintf("%x.token", string(room)))
}

func (f *Files) Read(room contract.RoomID) (string, error) {
	data, err := os.ReadFile(f.path(room))
	if err != nil {
		return "", errors.New("credential unavailable")
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("credential unavailable")
	}
	return token, nil
}

func (f *Files) Write(room contract.RoomID, token string) error {
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return errors.New("invalid token")
	}
	if err := os.MkdirAll(f.Dir, 0700); err != nil {
		return errors.New("cannot create credential directory")
	}
	file, err := os.CreateTemp(f.Dir, ".credential-*")
	if err != nil {
		return errors.New("cannot create credential file")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := io.WriteString(file, token); err != nil {
		return errors.New("cannot write credential file")
	}
	if err := file.Sync(); err != nil {
		return errors.New("cannot sync credential file")
	}
	if err := file.Close(); err != nil {
		return errors.New("cannot close credential file")
	}
	if err := os.Rename(file.Name(), f.path(room)); err != nil {
		return errors.New("cannot replace credential file")
	}
	return nil
}

func (f *Files) Clear(room contract.RoomID) error {
	err := os.Remove(f.path(room))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot remove credential file")
	}
	return nil
}

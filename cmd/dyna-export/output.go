package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/room"
	"github.com/resident-labs/dyna/contract"
)

func writeBundle(out string, global contract.GlobalBundle, rooms []contract.RoomBundle) (err error) {
	seen := make(map[contract.RoomName]bool)
	for _, part := range rooms {
		if room.ValidateName(string(part.Name)) != nil || seen[part.Name] {
			return fmt.Errorf("invalid or duplicate room name")
		}
		seen[part.Name] = true
	}
	if global.Rooms == nil || len(global.Rooms) != len(rooms) {
		return fmt.Errorf("room manifest does not match parts")
	}
	for _, name := range global.Rooms {
		if !seen[name] {
			return fmt.Errorf("room manifest does not match parts")
		}
		delete(seen, name)
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(out)
		}
	}()
	if err = os.Mkdir(filepath.Join(out, "rooms"), 0700); err != nil {
		return err
	}
	if err = writePart(filepath.Join(out, "global.json"), global); err != nil {
		return err
	}
	for _, part := range rooms {
		if err = writePart(filepath.Join(out, "rooms", string(part.Name)+".json"), part); err != nil {
			return err
		}
	}
	return nil
}

func writePart[T contract.GlobalBundle | contract.RoomBundle](path string, value T) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(value)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

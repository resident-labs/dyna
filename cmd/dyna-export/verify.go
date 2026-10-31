package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"dyna-agent/internal/room"
	"github.com/resident-labs/dyna/contract"
)

// verifyBundle decodes the output again and compares it with separate source
// queries. It does not call exporter selection functions.
func verifyBundle(db *sql.DB, bundle, backup string, cap capture) error {
	f, err := os.Open(filepath.Join(bundle, "global.json"))
	if err != nil {
		return err
	}
	global, err := contract.DecodeBundle[contract.GlobalBundle](f)
	f.Close()
	if err != nil {
		return err
	}
	backupFile, err := os.Open(backup)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, backupFile)
	backupFile.Close()
	if err != nil {
		return err
	}
	if global.Source.BackupSHA256 != hex.EncodeToString(hash.Sum(nil)) {
		return fmt.Errorf("bundle source backup hash differs")
	}
	if err := verifyInstructions(db, "*", global.Instructions); err != nil {
		return err
	}
	repos, err := countRepositories(db, cap)
	if err != nil {
		return err
	}
	if repos != len(global.Repos) {
		return fmt.Errorf("source repository count differs")
	}
	got := globalBundleCounts(global)
	if got != global.Counts {
		return fmt.Errorf("global declared counts differ from decoded records")
	}
	source, err := countGlobalSQL(db, cap)
	if err != nil {
		return err
	}
	// verifyInstructions already compared this document with source IDs and text.
	source.InstructionChars = utf8.RuneCountInString(global.Instructions)
	if got != source {
		return fmt.Errorf("global SQL counts differ from decoded records")
	}
	if global.Rooms == nil {
		return fmt.Errorf("missing room manifest")
	}
	names := make(map[string]bool, len(global.Rooms))
	for _, name := range global.Rooms {
		if room.ValidateName(string(name)) != nil || names[string(name)] {
			return fmt.Errorf("invalid or duplicate room name")
		}
		names[string(name)] = true
	}
	entries, err := os.ReadDir(filepath.Join(bundle, "rooms"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasSuffix(name, ".json") || !names[strings.TrimSuffix(name, ".json")] {
			return fmt.Errorf("unexpected room part")
		}
	}
	if len(entries) != len(names) {
		return fmt.Errorf("missing room part")
	}
	// Count rooms from SQLite independently of the manifest.
	var roomCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM room_records").Scan(&roomCount); err != nil {
		return err
	}
	if roomCount != len(names) {
		return fmt.Errorf("source room count differs")
	}
	for _, name := range global.Rooms {
		var roomID string
		err := db.QueryRow("SELECT id FROM room_records WHERE json_extract(body,'$.name')=?", name).Scan(&roomID)
		if err != nil {
			return fmt.Errorf("room missing from source: %w", err)
		}
		part, err := os.Open(filepath.Join(bundle, "rooms", string(name)+".json"))
		if err != nil {
			return err
		}
		value, err := contract.DecodeBundle[contract.RoomBundle](part)
		part.Close()
		if err != nil {
			return err
		}
		if value.Name != name {
			return fmt.Errorf("room part name mismatch")
		}
		if err := verifyInstructions(db, roomID, value.Instructions); err != nil {
			return err
		}
		counts, err := countRoomSQL(db, roomID)
		if err != nil {
			return err
		}
		if err := verifyRoomCounts(value, counts); err != nil {
			return err
		}
	}
	return nil
}

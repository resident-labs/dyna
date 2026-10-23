package runtime

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/account/store"
	"dyna-agent/internal/database"
)

// MigratePrivateHomes runs after accountstore.Migrate, before account readers
// start. It moves each Muse account's private-origin proof from the retained
// .private-login.json into the account's SQL metadata and keeps the file.
func MigratePrivateHomes(dataDir string) error {
	canonical, err := canonicalAccountDataRoot(dataDir)
	if err != nil {
		return err
	}
	dataDir = canonical
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "account.private-home", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		if ok, err := database.HasTable(ctx, conn, "accounts"); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("account private-home migration requires the account registry")
		}
		rows, err := conn.QueryContext(ctx, "SELECT id, metadata FROM accounts")
		if err != nil {
			return err
		}
		patches := map[string][]byte{}
		for rows.Next() {
			var id string
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			patched, err := importPrivateProof(dataDir, id, raw)
			if err != nil {
				rows.Close()
				return err
			}
			if patched != nil {
				patches[id] = patched
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for id, meta := range patches {
			if _, err := conn.ExecContext(ctx, "UPDATE accounts SET metadata = ? WHERE id = ?", meta, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// TODO: legacy/shim remove later — import retained private-login files once.
// importPrivateProof returns the account's metadata with the proof added, or
// nil when the account needs no change. SQL proof wins over the retained file.
func importPrivateProof(dataDir, id string, raw []byte) ([]byte, error) {
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, err
	}
	var harness, identity, home string
	for key, dst := range map[string]*string{"harness": &harness, "identity": &identity, "nativeHome": &home} {
		if v := meta[key]; len(v) != 0 {
			if err := json.Unmarshal(v, dst); err != nil {
				return nil, err
			}
		}
	}
	if !requirePrivateAccount(harness) || identity == "" {
		return nil, nil
	}
	if rawProof := meta["privateProof"]; len(rawProof) != 0 && string(rawProof) != "null" {
		var proof store.PrivateProof
		if json.Unmarshal(rawProof, &proof) != nil || !privateProofHolds(identity, &proof) {
			return nil, fmt.Errorf("invalid canonical private-home proof at %s", home)
		}
		return nil, nil
	}
	if len(id) != 32 {
		return nil, errAccountPath
	}
	if _, err := hex.DecodeString(id); err != nil {
		return nil, errAccountPath
	}
	canonical := filepath.Join(dataDir, "accounts", harness, id)
	if home != canonical && !equivalentAccountHome(filepath.Join(dataDir, "accounts"), harness, id, home) {
		return nil, nil
	}
	path := filepath.Join(canonical, ".private-login.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	}
	if err := verifyPrivateOrigin(path, identity); err != nil {
		return nil, fmt.Errorf("private home origin %s: %w", path, err)
	}
	proof, err := json.Marshal(store.PrivateProof{Version: 1, Identity: identity})
	if err != nil {
		return nil, err
	}
	meta["privateProof"] = proof
	return json.Marshal(meta)
}

// verifyPrivateOrigin accepts only the protected origin record that a private
// sign-in wrote for identity. It never returns the record's content.
func verifyPrivateOrigin(path, identity string) error {
	if err := checkPrivateAccountFile(path, false); err != nil {
		return err
	}
	// Reuse the checked, bounded snapshot reader for the same file safety rules.
	c, err := readAccountLoginFile(path)
	if err != nil {
		return errPrivateSignIn
	}
	var origin struct {
		Version  int    `json:"version"`
		Identity string `json:"identity"`
		Scope    string `json:"scope"`
	}
	if c.Credential.Schema != "dyna/private-home@1" || json.Unmarshal(c.Credential.Payload, &origin) != nil || origin.Version != 1 || origin.Identity != identity || c.Identity != identity || origin.Scope != "private-home-v1" {
		return errPrivateSignIn
	}
	return nil
}

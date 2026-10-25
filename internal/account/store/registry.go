package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/database"
	"dyna-agent/internal/harness/preferences"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
)

// The account registry lives in dyna.db (rule r10): one row per account
// holds its Metadata and its credential's schema, never a credential
// payload. Every payload is a secret or a reference to one, so it stays in
// one private 0600 file per account (credentialPath), outside dyna.db and
// its daily backups. The engine and a runner's failover switch write the
// registry; the switch writes from outside the engine until item 2 moves it
// into engine calls.
const registrySchema = `CREATE TABLE accounts (
 id TEXT PRIMARY KEY,
 ord INTEGER NOT NULL,
 metadata BLOB NOT NULL CHECK(json_valid(metadata)),
 credential_schema TEXT NOT NULL DEFAULT ''
);
CREATE TABLE accounts_registry (
 id INTEGER PRIMARY KEY CHECK(id = 1),
 version INTEGER NOT NULL,
 opencode_cleaned INTEGER NOT NULL
);`

// registryKey names accounts.json in state_mirrors.
const registryKey = "accounts/registry"

// Migrate creates the account registry tables. Only the engine runs it, at
// start.
func Migrate(dataDir string) error {
	_, err := openRegistry(dataDir)
	return err
}

func openRegistry(dataDir string) (*sql.DB, error) {
	return openRegistryAt(dataDir, filepath.Join(dataDir, "accounts"))
}

func openRegistryAt(dataDir, root string) (*sql.DB, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	if err := statewrite.MigrateMirrors(dataDir); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "account.registry", 5, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, registrySchema); err != nil {
				return err
			}
		}
		// TODO: legacy/shim remove later — the per-harness selection table moves
		// to the provider-scoped one in this version, and nothing reads it after.
		legacyActive, err := movedHarnessActive(ctx, conn)
		if err != nil {
			return err
		}
		// A folder whose rows the JSON import takes over is the only one whose
		// .active.json files still name its selections.
		jsonEra, err := registryIsEmpty(ctx, conn)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, activeSchema); err != nil {
			return err
		}
		if err := (&Store{Root: root}).importRegistry(ctx, conn); err != nil {
			return err
		}
		if err := fillProviders(ctx, conn); err != nil {
			return err
		}
		if err := fillActiveSelections(ctx, conn, legacyActive); err != nil {
			return err
		}
		if !jsonEra {
			return nil
		}
		return importActiveSelections(ctx, conn, root)
	})
	return db, err
}

// registryIsEmpty reports whether neither a row nor a migration record exists,
// so the JSON import owns this folder.
func registryIsEmpty(ctx context.Context, conn *sql.Conn) (bool, error) {
	var empty bool
	err := conn.QueryRowContext(ctx, `SELECT NOT (EXISTS(SELECT 1 FROM accounts) OR EXISTS(SELECT 1 FROM accounts_registry)
		OR EXISTS(SELECT 1 FROM state_mirrors WHERE key = ?))`, registryKey).Scan(&empty)
	return empty, err
}

// TODO: legacy/shim remove later — import retained selections only during migration.
// importActiveSelections gives each previous release's per-harness selection a
// row under its account's own provider. It reads a folder only when the JSON
// import takes it over, so a stale file never restores a selection that an
// owner already cleared in SQL.
func importActiveSelections(ctx context.Context, conn *sql.Conn, root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		harness := entry.Name()
		path := filepath.Join(root, harness, ".active.json")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		_, err := statewrite.ImportMigration(ctx, conn, activeKey(harness), path, func(conn *sql.Conn, raw []byte) error {
			var selection struct {
				ID string `json:"id"`
			}
			if len(raw) > 256 || json.Unmarshal(raw, &selection) != nil || !validActiveID(selection.ID) {
				return fmt.Errorf("invalid account selection for %s", harness)
			}
			// An empty ID records an explicit clear, which needs no row.
			if selection.ID == "" {
				return nil
			}
			provider, found, err := accountProviderOf(ctx, conn, selection.ID)
			if err != nil || !found {
				return err
			}
			return setActiveRow(ctx, conn, provider, "", selection.ID)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// accountProviderOf reports the provider a saved account names.
func accountProviderOf(ctx context.Context, conn *sql.Conn, id string) (Provider, bool, error) {
	var provider sql.NullString
	err := conn.QueryRowContext(ctx, "SELECT json_extract(metadata, '$.provider') FROM accounts WHERE id = ?", id).Scan(&provider)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return Provider(provider.String), true, nil
}

// TODO: legacy/shim remove later — one migration from per-harness selections
// to provider-scoped ones. movedHarnessActive renames the old table, if any,
// and reports whether it exists.
func movedHarnessActive(ctx context.Context, conn *sql.Conn) (bool, error) {
	var harness bool
	if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_table_info('account_active') WHERE name = 'harness')").Scan(&harness); err != nil {
		return false, err
	}
	if !harness {
		return false, nil
	}
	_, err := conn.ExecContext(ctx, "ALTER TABLE account_active RENAME TO account_active_harness")
	return err == nil, err
}

// fillProviders writes each row's provider and kind from ProviderFor, so the
// scoped table can key on the provider. A row that already names a provider
// keeps it.
func fillProviders(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "SELECT id, metadata FROM accounts")
	if err != nil {
		return err
	}
	type row struct {
		id   string
		meta []byte
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.meta); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range pending {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(r.meta, &fields); err != nil {
			return err
		}
		if named, ok := fieldValue(fields, "provider"); ok && named != "" {
			continue
		}
		harness, _ := fieldValue(fields, "harness")
		integration, _ := fieldValue(fields, "integration")
		provider, kind := ProviderFor(harness, integration)
		raw, err := json.Marshal(provider)
		if err != nil {
			return err
		}
		fields = setField(fields, "provider", raw)
		if raw, err = json.Marshal(kind); err != nil {
			return err
		}
		fields = setField(fields, "kind", raw)
		meta, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "UPDATE accounts SET metadata = ? WHERE id = ?", meta, r.id); err != nil {
			return err
		}
	}
	return nil
}

// fieldValue reads one metadata string field, matching its name without case.
func fieldValue(fields map[string]json.RawMessage, name string) (string, bool) {
	for key, raw := range fields {
		if !strings.EqualFold(key, name) {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return "", false
		}
		return value, true
	}
	return "", false
}

// setField writes one metadata field under its canonical name, dropping any
// other spelling of it.
func setField(fields map[string]json.RawMessage, name string, raw json.RawMessage) map[string]json.RawMessage {
	for key := range fields {
		if strings.EqualFold(key, name) {
			delete(fields, key)
		}
	}
	fields[name] = raw
	return fields
}

// fillActiveSelections copies each per-harness row to its active account's
// provider at global scope, then drops the old table.
func fillActiveSelections(ctx context.Context, conn *sql.Conn, legacy bool) error {
	if !legacy {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO account_active(provider, scope, account)
		SELECT json_extract(a.metadata, '$.provider'), '', old.account
		FROM account_active_harness old JOIN accounts a ON a.id = old.account
		ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "DROP TABLE account_active_harness")
	return err
}

// Topic is the committed event of a changed account registry. The engine's
// account timer wakes on it.
const Topic = "accounts"

// db is the registry's database: dyna.db in the folder above the accounts.
func (s *Store) db() (*sql.DB, error) { return openRegistryAt(filepath.Dir(s.Root), s.Root) }

func (s *Store) registryPath() string { return filepath.Join(s.Root, "accounts.json") }

// credentialDir holds one private file per account's credential.
func (s *Store) credentialDir() string { return filepath.Join(s.Root, "credentials") }

func (s *Store) credentialPath(id string) string {
	return filepath.Join(s.credentialDir(), id+".json")
}

// credentialStep runs between the steps of a registry write. Tests replace
// it to stop the write there, as a crash would.
var credentialStep = func(step string) error { return nil }

// clone copies st's entry list, so a later change to it does not alter a
// committed state.
func (st State) clone() State {
	st.Entries = append([]Entry(nil), st.Entries...)
	return st
}

// load reads the registry rows and each account's credential file. A row
// whose credential file is missing or damaged reads as signed out.
func (s *Store) load(ctx context.Context, db *sql.DB) (State, error) {
	st := State{Version: 1, Entries: []Entry{}}
	err := statewrite.For(db).Read(ctx, func(q statewrite.Reader) error {
		var cleaned int
		err := q.QueryRowContext(ctx, "SELECT version, opencode_cleaned FROM accounts_registry WHERE id = 1").Scan(&st.Version, &cleaned)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		st.OpenCodeProvidersCleaned = cleaned != 0
		rows, err := q.QueryContext(ctx, "SELECT metadata, credential_schema FROM accounts ORDER BY ord")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			meta, schema, err := scanMetadata(rows)
			if err != nil {
				return err
			}
			e := Entry{Metadata: meta}
			if schema != "" {
				c, ok := s.readCredential(e.ID)
				if ok && c.Schema == schema {
					e.Credential = c
				} else {
					e.NeedsLogin = true
				}
			}
			st.Entries = append(st.Entries, e)
		}
		return rows.Err()
	})
	if err != nil {
		return State{}, errors.New("Saved accounts could not be read.")
	}
	return st, nil
}

// ReadMetadata reads saved metadata through the caller's database snapshot.
// It does not read credential files or import the previous release's registry.
func ReadMetadata(ctx context.Context, q statewrite.Reader, id string) (Metadata, error) {
	meta, _, err := scanMetadata(q.QueryRowContext(ctx, "SELECT metadata, credential_schema FROM accounts WHERE id = ?", id))
	return meta, err
}

func scanMetadata(row interface{ Scan(...any) error }) (Metadata, string, error) {
	var meta Metadata
	var raw []byte
	var schema string
	if err := row.Scan(&raw, &schema); err != nil {
		return meta, "", err
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Metadata{}, "", err
	}
	return meta, schema, nil
}

// readCredential reads account id's credential file; ok is false when it is
// missing or damaged.
func (s *Store) readCredential(id string) (Credential, bool) {
	f, err := os.OpenFile(s.credentialPath(id), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return Credential{}, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxStoreBytes+1))
	if err != nil || len(raw) > maxStoreBytes {
		return Credential{}, false
	}
	var c Credential
	if json.Unmarshal(raw, &c) != nil || c.Schema == "" {
		return Credential{}, false
	}
	return c, true
}

// writeCredential is the one writer of an account's credential file: login,
// refresh, a switch checkpoint, legacy grant moves and an import all go
// through save, which calls it under accounts.lock. The folder is made 0700
// and the file 0600 at creation, and the file is replaced atomically.
func (s *Store) writeCredential(id string, c Credential) error {
	if c.Schema == "" {
		return s.removeCredential(id)
	}
	if err := os.Mkdir(s.credentialDir(), 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.credentialDir(), ".credential-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // Gone after the rename.
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), s.credentialPath(id)); err != nil {
		return err
	}
	return s.syncCredentialDir()
}

func (s *Store) removeCredential(id string) error {
	if err := os.Remove(s.credentialPath(id)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return s.syncCredentialDir()
}

// syncCredentialDir makes a credential file's rename or removal durable
// before save commits the row that describes it. SQLite makes the row
// durable; without this sync, a power loss could keep the row and lose the
// rename, leaving metadata newer than its secret.
func (s *Store) syncCredentialDir() error {
	dir, err := os.Open(s.credentialDir())
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// save commits next over committed. The order keeps two rules: never a
// secret without a row, and never metadata newer than its secret.
//   - An account whose credential changed writes its new secret, then its row.
//   - A new account writes its row, then its secret, so a crash between the
//     two leaves a row that reads as signed out.
//   - A removed account loses its row, then its secret.
func (s *Store) save(ctx context.Context, db *sql.DB, committed, next State) error {
	old := map[string]Entry{}
	for _, e := range committed.Entries {
		old[e.ID] = e
	}
	keep := map[string]bool{}
	changed, err := s.rowsChanged(ctx, db, next)
	if err != nil {
		return errors.New("Saved accounts could not be written.")
	}
	for _, e := range next.Entries {
		keep[e.ID] = true
		if prior, ok := old[e.ID]; ok && !sameCredential(prior.Credential, e.Credential) {
			changed = true
			if err := s.writeCredential(e.ID, e.Credential); err != nil {
				return errors.New("Saved accounts could not be written.")
			}
		}
	}
	if err := credentialStep("updated secrets"); err != nil {
		return err
	}
	var removed []string
	for id := range old {
		if !keep[id] {
			removed = append(removed, id)
		}
	}
	// Only a changed registry wakes the engine, so a pass that finds nothing
	// to do does not wake the next one.
	var event hub.Event
	if changed && !s.Quiet {
		event.Topic = Topic
	}
	if err := statewrite.For(db).Write(ctx, event, func(conn *sql.Conn) error {
		if err := writeRows(ctx, conn, next); err != nil {
			return err
		}
		return forgetAccounts(ctx, conn, removed)
	}); err != nil {
		if errors.Is(err, statewrite.ErrRepeated) || errors.Is(err, statewrite.ErrRequestConflict) {
			return err
		}
		return errors.New("Saved accounts could not be written.")
	}
	for _, e := range next.Entries {
		if _, ok := old[e.ID]; !ok {
			if err := s.writeCredential(e.ID, e.Credential); err != nil {
				return errors.New("Saved accounts could not be written.")
			}
		}
	}
	for _, id := range removed {
		if err := s.removeCredential(id); err != nil {
			return errors.New("Saved accounts could not be written.")
		}
	}
	return nil
}

// rowsChanged reports whether next differs from the stored account rows.
func (s *Store) rowsChanged(ctx context.Context, db *sql.DB, next State) (bool, error) {
	type row struct {
		metadata string
		schema   string
	}
	var stored []row
	err := statewrite.For(db).Read(ctx, func(q statewrite.Reader) error {
		rows, err := q.QueryContext(ctx, "SELECT metadata, credential_schema FROM accounts ORDER BY ord")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.metadata, &r.schema); err != nil {
				return err
			}
			stored = append(stored, r)
		}
		return rows.Err()
	})
	if err != nil || len(stored) != len(next.Entries) {
		return true, err
	}
	for i, e := range next.Entries {
		raw, err := mergeMetadata(e.Metadata, []byte(stored[i].metadata))
		if err != nil {
			return false, err
		}
		if stored[i].metadata != string(raw) || stored[i].schema != e.Credential.Schema {
			return true, nil
		}
	}
	return false, nil
}

// writeRows changes only new, changed, moved, or removed account rows. The
// caller holds accounts.lock, and unknown metadata remains in each saved row.
func writeRows(ctx context.Context, conn *sql.Conn, st State) error {
	type storedRow struct {
		ord      int
		metadata []byte
		schema   string
	}
	prior := map[string]storedRow{}
	rows, err := conn.QueryContext(ctx, "SELECT id, ord, metadata, credential_schema FROM accounts")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var row storedRow
		if err := rows.Scan(&id, &row.ord, &row.metadata, &row.schema); err != nil {
			rows.Close()
			return err
		}
		prior[id] = row
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for i, e := range st.Entries {
		row, exists := prior[e.ID]
		raw, err := mergeMetadata(e.Metadata, row.metadata)
		if err != nil {
			return err
		}
		delete(prior, e.ID)
		if exists && row.ord == i && bytes.Equal(row.metadata, raw) && row.schema == e.Credential.Schema {
			continue
		}
		if exists {
			_, err = conn.ExecContext(ctx, "UPDATE accounts SET ord = ?, metadata = ?, credential_schema = ? WHERE id = ?", i, raw, e.Credential.Schema, e.ID)
		} else {
			_, err = conn.ExecContext(ctx, "INSERT INTO accounts(id, ord, metadata, credential_schema) VALUES (?, ?, ?, ?)", e.ID, i, raw, e.Credential.Schema)
		}
		if err != nil {
			return err
		}
	}
	for id := range prior {
		if _, err := conn.ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", id); err != nil {
			return err
		}
	}
	cleaned := 0
	if st.OpenCodeProvidersCleaned {
		cleaned = 1
	}
	version := st.Version
	if version == 0 {
		version = 1
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO accounts_registry(id, version, opencode_cleaned) VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET version = excluded.version, opencode_cleaned = excluded.opencode_cleaned
WHERE accounts_registry.version != excluded.version OR accounts_registry.opencode_cleaned != excluded.opencode_cleaned`, version, cleaned)
	return err
}

func sameCredential(a, b Credential) bool {
	return a.Schema == b.Schema && bytes.Equal(a.Payload, b.Payload)
}

// mergeMetadata preserves fields that this release does not understand.
func mergeMetadata(meta Metadata, prior []byte) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	if len(prior) != 0 {
		if err := json.Unmarshal(prior, &fields); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	var next map[string]json.RawMessage
	if err := json.Unmarshal(raw, &next); err != nil {
		return nil, err
	}
	typ := reflect.TypeOf(meta)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		replacement, present := next[name]
		for key, retained := range fields {
			if !strings.EqualFold(key, name) {
				continue
			}
			delete(fields, key)
			if !present {
				continue
			}
			// Keep the raw value of an unchanged field. Re-encoding an
			// observation would discard nested members this release cannot read.
			value := reflect.New(field.Type)
			if err := json.Unmarshal(retained, value.Interface()); err != nil {
				return nil, err
			}
			known, err := json.Marshal(value.Interface())
			if err != nil {
				return nil, err
			}
			if bytes.Equal(known, next[name]) {
				replacement = retained
			}
		}
		if present {
			fields[name] = replacement
		}
	}
	return json.Marshal(fields)
}

// TODO: legacy/shim remove later — import accounts.json once, before SQL owns the registry.
// Credential files are durable before the transaction commits metadata. Retrying
// an interrupted migration preserves any credential file already written.
func (s *Store) importRegistry(ctx context.Context, conn *sql.Conn) error {
	var canonical bool
	if err := conn.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM accounts) OR EXISTS(SELECT 1 FROM accounts_registry)
		OR EXISTS(SELECT 1 FROM state_mirrors WHERE key = ?)`, registryKey).Scan(&canonical); err != nil {
		return err
	}
	if canonical {
		return nil
	}
	_, err := statewrite.ImportMigration(ctx, conn, registryKey, s.registryPath(), func(conn *sql.Conn, raw []byte) error {
		st, err := parseRegistry(raw)
		if err != nil {
			return err
		}
		var source struct{ Entries []map[string]json.RawMessage }
		if err := json.Unmarshal(raw, &source); err != nil {
			return err
		}
		for i, e := range st.Entries {
			if e.ID == "" || filepath.Base(e.ID) != e.ID || e.ID == "." || e.ID == ".." {
				return errors.New("Saved accounts have an unsupported or invalid format.")
			}
			if _, err := os.Lstat(s.credentialPath(e.ID)); errors.Is(err, os.ErrNotExist) {
				if err := s.writeCredential(e.ID, e.Credential); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			// JSON field matching is case-insensitive; no credential spelling may
			// enter the metadata row.
			for name := range source.Entries[i] {
				if strings.EqualFold(name, "credential") {
					delete(source.Entries[i], name)
				}
			}
			schema := e.Credential.Schema
			if c, ok := s.readCredential(e.ID); ok {
				schema = c.Schema
			}
			metadata, err := json.Marshal(source.Entries[i])
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, "INSERT INTO accounts(id,ord,metadata,credential_schema) VALUES(?,?,?,?)", e.ID, i, metadata, schema); err != nil {
				return err
			}
		}
		cleaned := 0
		if st.OpenCodeProvidersCleaned {
			cleaned = 1
		}
		_, err = conn.ExecContext(ctx, "INSERT INTO accounts_registry(id,version,opencode_cleaned) VALUES(1,?,?)", st.Version, cleaned)
		return err
	})
	return err
}

func parseRegistry(raw []byte) (State, error) {
	if len(raw) > maxStoreBytes {
		return State{}, errors.New("Saved accounts could not be read.")
	}
	var st State
	if json.Unmarshal(raw, &st) != nil || st.Version != 1 {
		return State{}, errors.New("Saved accounts have an unsupported or invalid format.")
	}
	return st, nil
}

// dropOrphanCredentials deletes the credential file of every account that
// has no row, such as one the previous release removed.
func (s *Store) dropOrphanCredentials(ctx context.Context, db *sql.DB) error {
	ids := map[string]bool{}
	err := statewrite.For(db).Read(ctx, func(q statewrite.Reader) error {
		rows, err := q.QueryContext(ctx, "SELECT id FROM accounts")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids[id] = true
		}
		return rows.Err()
	})
	if err != nil {
		return errors.New("Saved accounts could not be read.")
	}
	entries, err := os.ReadDir(s.credentialDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("Saved accounts could not be read.")
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || ids[id] {
			continue
		}
		if err := s.removeCredential(id); err != nil {
			return errors.New("Saved accounts could not be written.")
		}
	}
	return nil
}

// forgetters delete the rows that each store keeps per account. A removal
// runs them in the account's own write (rule 6), and each owner keeps its
// table names.
var forgetters = []func(ctx context.Context, conn *sql.Conn, id string) error{
	forgetActive,
	reposcope.Forget,
	preferences.Forget,
}

func forgetAccounts(ctx context.Context, conn *sql.Conn, ids []string) error {
	for _, id := range ids {
		for _, forget := range forgetters {
			if err := forget(ctx, conn, id); err != nil {
				return err
			}
		}
	}
	return nil
}

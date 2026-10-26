package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
);`

// Migrate creates the account registry tables. Only the engine runs it, at
// start.
func Migrate(dataDir string) error {
	_, err := openRegistry(dataDir)
	return err
}

func openRegistry(dataDir string) (*sql.DB, error) {
	return openRegistryAt(dataDir)
}

func openRegistryAt(dataDir string) (*sql.DB, error) {
	db, err := database.Open(dataDir)
	if err != nil {
		return nil, err
	}
	if err := statewrite.MigrateMirrors(dataDir); err != nil {
		return nil, err
	}
	err = database.Migrate(db, "account.registry", 9, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old < 1 {
			if _, err := conn.ExecContext(ctx, registrySchema); err != nil {
				return err
			}
		}
		// Version 8 moves the per-harness selection table to the provider-scoped
		// one and retires the JSON registry. It runs only for a database older
		// than 8: the table it converts is the legacy one, which has no provider
		// column, so running it again would read the new table as legacy and
		// collapse room selections into the global scope. Version 9 then drops
		// the rows of a harness this release stopped running.
		if old < 8 {
			// TODO: legacy/shim remove later — the per-harness selection table moves
			// to the provider-scoped one in this version, and nothing reads it after.
			legacyActive, err := movedHarnessActive(ctx, conn)
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, activeSchema); err != nil {
				return err
			}
			if err := fillProviders(ctx, conn); err != nil {
				return err
			}
			if err := fillActiveSelections(ctx, conn, legacyActive); err != nil {
				return err
			}
			if err := fillActiveHarness(ctx, conn); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, "DROP TABLE IF EXISTS accounts_registry"); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, "DELETE FROM state_mirrors WHERE key = 'accounts/registry'"); err != nil {
				return err
			}
		}
		if old < 9 {
			if err := dropRemovedHarnessRows(ctx, conn); err != nil {
				return err
			}
		}
		_, err = conn.ExecContext(ctx, piSplitSchema)
		return err
	})
	return db, err
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

// dropRemovedHarnessRows retires the rows of harnesses this release does not run:
// Codex, Muse and OpenCode, whose providers Pi took over, and any harness that
// left the catalog. A retired row is dropped only when a Pi account already holds
// the same provider sign-in, so retiring a harness never discards a saved owner
// credential that nobody has moved yet. A row with no such replacement stays, and
// the account views report it as an unknown-harness account the owner can repair
// or remove; the importer can still read it for that repair. A dropped account
// loses its active marks, repository scope, limit and, through the orphan cleanup,
// its credential file. The engine backs up dyna.db before this migration.
func dropRemovedHarnessRows(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "SELECT id, json_extract(CAST(metadata AS TEXT), '$.harness'), json_extract(CAST(metadata AS TEXT), '$.provider'), json_extract(CAST(metadata AS TEXT), '$.identity'), credential_schema, json_extract(CAST(metadata AS TEXT), '$.needsLogin') FROM accounts")
	if err != nil {
		return err
	}
	// replacements holds the provider sign-ins a signed-in Pi account covers. A Pi
	// slot the owner has not signed in yet covers nothing: it carries no identity
	// and no credential, so it cannot have taken over another sign-in (D-125).
	replacements := map[string]map[string]bool{}
	type candidate struct{ id, harness, provider, identity string }
	var candidates []candidate
	for rows.Next() {
		var id, harness, provider, identity, schema sql.NullString
		var needsLogin sql.NullBool
		if err := rows.Scan(&id, &harness, &provider, &identity, &schema, &needsLogin); err != nil {
			rows.Close()
			return err
		}
		if KnownHarness(harness.String) && !AccountsRetired(harness.String) {
			if harness.String == "pi" && provider.String != "" && identity.String != "" && schema.String != "" && !needsLogin.Bool {
				if replacements[provider.String] == nil {
					replacements[provider.String] = map[string]bool{}
				}
				replacements[provider.String][identity.String] = true
			}
			continue
		}
		candidates = append(candidates, candidate{id.String, harness.String, provider.String, identity.String})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var ids []string
	retiredHarnesses := map[string]bool{"": true}
	for _, row := range candidates {
		retiredHarnesses[row.harness] = true
		if !replacedByPi(replacements[row.provider], row.identity) {
			continue
		}
		ids = append(ids, row.id)
	}
	for _, id := range ids {
		if _, err := conn.ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", id); err != nil {
			return err
		}
	}
	if err := forgetAccounts(ctx, conn, ids); err != nil {
		return err
	}
	// forgetters clear a selection but keep its row, so a room can override the
	// global choice with none. A selection of a harness this release does not run
	// has no such meaning and goes, whether or not its account row was kept.
	harnesses := make([]string, 0, len(retiredHarnesses))
	for harness := range retiredHarnesses {
		harnesses = append(harnesses, harness)
	}
	sort.Strings(harnesses)
	for _, harness := range harnesses {
		if _, err := conn.ExecContext(ctx, "DELETE FROM account_active WHERE harness = ?", harness); err != nil {
			return err
		}
	}
	return nil
}

// replacedByPi reports whether a signed-in Pi account covers this provider
// sign-in: the same provider and the same known identity. An empty identity is
// never a wildcard, because a Pi slot the owner has not signed in has none, and a
// sign-in the migration cannot match must be kept rather than discarded.
func replacedByPi(covered map[string]bool, identity string) bool {
	return identity != "" && covered[identity]
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
	// The legacy table kept one row per harness, so its key names the harness of
	// the selection and each harness keeps its own choice.
	if _, err := conn.ExecContext(ctx, `INSERT INTO account_active(provider, harness, scope, account)
		SELECT json_extract(a.metadata, '$.provider'), old.harness, '', old.account
		FROM account_active_harness old JOIN accounts a ON a.id = old.account
		ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "DROP TABLE account_active_harness")
	return err
}

// fillActiveHarness rebuilds the selection table with the harness in its key,
// so that two harnesses running one provider keep their own choice. Each row's
// harness is the harness of the account it names. A row whose account is gone
// keeps no harness and so selects nothing.
func fillActiveHarness(ctx context.Context, conn *sql.Conn) error {
	var hasHarness bool
	if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_table_info('account_active') WHERE name = 'harness')").Scan(&hasHarness); err != nil {
		return err
	}
	if hasHarness {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE account_active_harness_v7 (
 provider TEXT NOT NULL,
 harness TEXT NOT NULL DEFAULT '',
 scope TEXT NOT NULL DEFAULT '',
 account TEXT NOT NULL,
 PRIMARY KEY (provider, harness, scope))`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO account_active_harness_v7(provider, harness, scope, account)
		SELECT old.provider, COALESCE(json_extract(a.metadata, '$.harness'), ''), old.scope, old.account
		FROM account_active old LEFT JOIN accounts a ON a.id = old.account`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "DROP TABLE account_active"); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "ALTER TABLE account_active_harness_v7 RENAME TO account_active")
	return err
}

// piSplitSchema keeps the one-time Pi split in dyna.db: the accounts it moves
// providers out of, their entries and repository rules, and the account each
// provider was moved into. It is deleted with the split step.
const piSplitSchema = `CREATE TABLE IF NOT EXISTS pi_split_sources (
 account TEXT PRIMARY KEY,
 home TEXT NOT NULL,
 label TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS pi_split_source_entries (
 source TEXT NOT NULL,
 provider TEXT NOT NULL,
 PRIMARY KEY (source, provider)
);
CREATE TABLE IF NOT EXISTS pi_split_source_repos (
 source TEXT NOT NULL,
 repo TEXT NOT NULL,
 PRIMARY KEY (source, repo)
);
CREATE TABLE IF NOT EXISTS pi_split_done (
 source TEXT NOT NULL,
 provider TEXT NOT NULL,
 account TEXT NOT NULL,
 completed INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (source, provider)
);`

// Topic is the committed event of a changed account registry. The engine's
// account timer wakes on it.
const Topic = "accounts"

// db is the registry's database: dyna.db in the folder above the accounts.
func (s *Store) db() (*sql.DB, error) { return openRegistryAt(filepath.Dir(s.Root)) }

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
	st := State{Entries: []Entry{}}
	err := statewrite.For(db).Read(ctx, func(q statewrite.Reader) error {
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
	return nil
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

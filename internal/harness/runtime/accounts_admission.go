package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/account/store"
)

// preparedAccount retains only the saved inputs used for account admission.
// The caller holds the existing turn lease through validation and admission.
type preparedAccount struct {
	root     string
	repo     string
	metadata store.Metadata
	repos    []string
}

var errPreparedAccountChanged = errors.New("The prepared account changed. Prepare the turn again.")

// CheckPreparedAccount validates the already selected account before admission.
// The caller must hold its existing PrepareRuntime lease. This check never
// selects an account. File and credential checks happen only here.
func (a *Accounts) CheckPreparedAccount(harness, accountID, repo string) (NativeAccount, error) {
	n, err := a.activeFor(harness, repo)
	if err != nil {
		return NativeAccount{}, err
	}
	if n.ID != accountID || !n.Active {
		return NativeAccount{}, errPreparedAccountChanged
	}
	if n.Status != "ready" {
		return NativeAccount{}, native.ErrNotSignedIn
	}
	var metadata store.Metadata
	err = a.transaction(func(s *store.State) error {
		e := s.Find(n.ID)
		if e == nil {
			return errAccountMissing
		}
		if e.Harness != n.Harness || e.Identity != n.Identity || e.NeedsLogin ||
			e.NativeHome != n.Home && !equivalentAccountHome(a.Root, n.Harness, n.ID, e.NativeHome) {
			return errPreparedAccountChanged
		}
		metadata = e.Metadata
		return nil
	}, false)
	if err != nil {
		return NativeAccount{}, err
	}
	login, err := readAccountLogin(n.Home)
	if err != nil {
		return NativeAccount{}, err
	}
	if login.Identity != metadata.Identity || login.Integration != metadata.Integration {
		return NativeAccount{}, errPreparedAccountChanged
	}
	rules, err := a.rules()
	if err != nil {
		return NativeAccount{}, err
	}
	if !rules.Allows(n.ID, repo) {
		return NativeAccount{}, errScopeRefused
	}
	n.Repos = slices.Clone(rules[n.ID])
	n.prepared = &preparedAccount{root: a.Root, repo: repo, metadata: metadata, repos: slices.Clone(n.Repos)}
	return n, nil
}

// RecheckPreparedAccount verifies the validated snapshot inside state.Write.
// It reads only conn. It performs no file, credential, network or migration I/O.
// expected must be the unchanged result of CheckPreparedAccount.
func (a *Accounts) RecheckPreparedAccount(ctx context.Context, conn *sql.Conn, expected NativeAccount, repo string) error {
	snapshot := expected.prepared
	if snapshot == nil || snapshot.root != a.Root || snapshot.repo != repo {
		return errPreparedAccountChanged
	}
	old := snapshot.metadata
	if expected.ID != old.ID || expected.Harness != old.Harness || expected.Identity != old.Identity ||
		expected.Home != filepath.Join(a.Root, old.Harness, old.ID) || expected.Status != "ready" ||
		!expected.Active || !slices.Equal(expected.Repos, snapshot.repos) {
		return errPreparedAccountChanged
	}
	current, err := store.ReadMetadata(ctx, conn, old.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return errAccountMissing
	}
	if err != nil {
		return err
	}
	if current.ID != old.ID || current.Harness != old.Harness || current.Identity != old.Identity ||
		current.NativeHome != old.NativeHome || current.Integration != old.Integration || current.NeedsLogin {
		return errPreparedAccountChanged
	}
	active, err := store.ReadActive(ctx, conn, accountProvider(old.Harness), "")
	if err != nil {
		return err
	}
	if active != old.ID {
		return errPreparedAccountChanged
	}
	rules, err := reposcope.ReadRules(ctx, conn)
	if err != nil {
		return err
	}
	if !rules.Allows(old.ID, repo) {
		return fmt.Errorf("%w: prepare the turn again", errScopeRefused)
	}
	if !slices.Equal(rules[old.ID], snapshot.repos) {
		return errPreparedAccountChanged
	}
	return nil
}

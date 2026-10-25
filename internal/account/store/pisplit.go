package store

import (
	"context"
	"database/sql"

	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
)

// PiSplitSource is one Pi account the one-time split moves providers out of,
// with the entries it holds and the repositories it may run in. The split keeps
// this plan in dyna.db, so an interrupted start resumes with the same accounts,
// entries and rules.
type PiSplitSource struct {
	Account string
	Home    string
	Label   string
	Entries []string
	Repos   []string
}

// PiSplitKey names one provider entry of one source account.
type PiSplitKey struct {
	Source   string
	Provider string
}

// PiSplitStep is the state of one entry of the split. Account is the account the
// entry was allocated to; Completed means every step of the move is done, so no
// later start touches that account again and owner changes to it stay.
type PiSplitStep struct {
	Account   string
	Completed bool
}

// PiSplitSources reads the saved plan of the split.
func (s *Store) PiSplitSources(ctx context.Context) ([]PiSplitSource, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	var sources []PiSplitSource
	err = statewrite.For(db).Read(ctx, func(q statewrite.Reader) error {
		rows, err := q.QueryContext(ctx, "SELECT account, home, label FROM pi_split_sources ORDER BY account")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var src PiSplitSource
			if err := rows.Scan(&src.Account, &src.Home, &src.Label); err != nil {
				return err
			}
			sources = append(sources, src)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range sources {
			if sources[i].Entries, err = piSplitColumn(ctx, q, "pi_split_source_entries", "provider", sources[i].Account); err != nil {
				return err
			}
			if sources[i].Repos, err = piSplitColumn(ctx, q, "pi_split_source_repos", "repo", sources[i].Account); err != nil {
				return err
			}
		}
		return nil
	})
	return sources, err
}

// piSplitColumn reads one string column of a split plan table for a source.
func piSplitColumn(ctx context.Context, q statewrite.Reader, table, column, source string) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+column+" FROM "+table+" WHERE source = ? ORDER BY "+column, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// SavePiSplitSource records one source and its entries and rules, replacing any
// earlier copy.
func (s *Store) SavePiSplitSource(ctx context.Context, src PiSplitSource) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `INSERT INTO pi_split_sources(account, home, label) VALUES (?, ?, ?)
ON CONFLICT(account) DO UPDATE SET home = excluded.home, label = excluded.label`, src.Account, src.Home, src.Label); err != nil {
			return err
		}
		for _, table := range []string{"pi_split_source_entries", "pi_split_source_repos"} {
			if _, err := conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE source = ?", src.Account); err != nil {
				return err
			}
		}
		for _, entry := range src.Entries {
			if _, err := conn.ExecContext(ctx, "INSERT INTO pi_split_source_entries(source, provider) VALUES (?, ?)", src.Account, entry); err != nil {
				return err
			}
		}
		for _, repo := range src.Repos {
			if _, err := conn.ExecContext(ctx, "INSERT INTO pi_split_source_repos(source, repo) VALUES (?, ?)", src.Account, repo); err != nil {
				return err
			}
		}
		return nil
	})
}

// PiSplitDone reads the state of every entry the split planned.
func (s *Store) PiSplitDone(ctx context.Context) (map[PiSplitKey]PiSplitStep, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	done := map[PiSplitKey]PiSplitStep{}
	err = statewrite.For(db).Read(ctx, func(q statewrite.Reader) error {
		rows, err := q.QueryContext(ctx, "SELECT source, provider, account, completed FROM pi_split_done")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key PiSplitKey
			var step PiSplitStep
			if err := rows.Scan(&key.Source, &key.Provider, &step.Account, &step.Completed); err != nil {
				return err
			}
			done[key] = step
		}
		return rows.Err()
	})
	return done, err
}

// MarkPiSplitDone records the account allocated to a provider entry. Completion
// is recorded separately, after every step of the move ran.
func (s *Store) MarkPiSplitDone(ctx context.Context, key PiSplitKey, account string) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO pi_split_done(source, provider, account, completed) VALUES (?, ?, ?, 0)
ON CONFLICT(source, provider) DO UPDATE SET account = excluded.account, completed = 0`, key.Source, key.Provider, account)
		return err
	})
}

// CompletePiSplit records that every step of one entry is done, so no later
// start moves or repairs it again.
func (s *Store) CompletePiSplit(ctx context.Context, key PiSplitKey) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "UPDATE pi_split_done SET completed = 1 WHERE source = ? AND provider = ?", key.Source, key.Provider)
		return err
	})
}

// ClearPiSplitDone drops the record of an entry whose credential is gone.
func (s *Store) ClearPiSplitDone(ctx context.Context, key PiSplitKey) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM pi_split_done WHERE source = ? AND provider = ?", key.Source, key.Provider)
		return err
	})
}

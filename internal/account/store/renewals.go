package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
)

const renewalHistoryLimit = 20

// RenewalResult is the classified outcome of one engine credential renewal.
// Providers never supply these values or the stored reason.
type RenewalResult string

const (
	RenewalResultRenewed      RenewalResult = "renewed"
	RenewalResultNoCredential RenewalResult = "no_credential"
	RenewalResultInvalidGrant RenewalResult = "invalid_grant"
	RenewalResultTransient    RenewalResult = "transient"
)

// Renewal records an engine attempt, never a renewal a harness performs alone.
// Reason is short, classified text and contains no provider response or token.
type Renewal struct {
	AttemptedAt time.Time     `json:"attemptedAt"`
	Result      RenewalResult `json:"result"`
	Reason      string        `json:"reason,omitempty"`
	AccountID   string        `json:"accountId"`
	Harness     string        `json:"harness"`
	Provider    Provider      `json:"provider,omitempty"`
}

const renewalSchema = `CREATE TABLE account_renewals (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 account_id TEXT NOT NULL,
 harness TEXT NOT NULL,
 provider TEXT NOT NULL,
 attempted_at INTEGER NOT NULL,
 result TEXT NOT NULL CHECK(result IN ('renewed','no_credential','invalid_grant','transient')),
 reason TEXT NOT NULL CHECK(length(reason) <= 160),
 FOREIGN KEY(account_id) REFERENCES accounts(id) ON DELETE CASCADE
);
CREATE INDEX account_renewals_account_time
ON account_renewals(account_id, attempted_at DESC, id DESC);`

// RecordRenewal appends one attempt and retains only the latest 20 per account.
func (s *Store) RecordRenewal(ctx context.Context, renewal Renewal) error {
	if renewal.AccountID == "" || renewal.Harness == "" || renewal.AttemptedAt.IsZero() {
		return errors.New("A renewal needs an account, harness and attempt time.")
	}
	switch renewal.Result {
	case RenewalResultRenewed, RenewalResultNoCredential, RenewalResultInvalidGrant, RenewalResultTransient:
	default:
		return errors.New("A renewal has an unknown result.")
	}
	if len(renewal.Reason) > 160 {
		return errors.New("A renewal reason is too long.")
	}
	renewal.AttemptedAt = renewal.AttemptedAt.Round(0).UTC()
	db, err := s.db()
	if err != nil {
		return errors.New("Saved account renewal could not be written.")
	}
	err = statewrite.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `INSERT INTO account_renewals
(account_id,harness,provider,attempted_at,result,reason) VALUES(?,?,?,?,?,?)`,
			renewal.AccountID, renewal.Harness, renewal.Provider, renewal.AttemptedAt.UnixNano(), renewal.Result, renewal.Reason); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `DELETE FROM account_renewals WHERE account_id=? AND id NOT IN (
SELECT id FROM account_renewals WHERE account_id=? ORDER BY attempted_at DESC,id DESC LIMIT ?)`,
			renewal.AccountID, renewal.AccountID, renewalHistoryLimit)
		return err
	})
	if err != nil {
		return errors.New("Saved account renewal could not be written.")
	}
	return nil
}

// LatestRenewals returns the latest stored attempt for every account.
func (s *Store) LatestRenewals(ctx context.Context) (map[string]Renewal, error) {
	db, err := s.db()
	if err != nil {
		return nil, errors.New("Saved account renewals could not be read.")
	}
	latest := map[string]Renewal{}
	err = statewrite.For(db).Read(ctx, func(reader statewrite.Reader) error {
		rows, err := reader.QueryContext(ctx, `SELECT account_id,harness,provider,attempted_at,result,reason FROM (
SELECT id,account_id,harness,provider,attempted_at,result,reason,
ROW_NUMBER() OVER (PARTITION BY account_id ORDER BY attempted_at DESC,id DESC) AS position
FROM account_renewals) WHERE position=1`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var renewal Renewal
			var attemptedAt int64
			if err := rows.Scan(&renewal.AccountID, &renewal.Harness, &renewal.Provider, &attemptedAt, &renewal.Result, &renewal.Reason); err != nil {
				return err
			}
			renewal.AttemptedAt = time.Unix(0, attemptedAt).UTC()
			latest[renewal.AccountID] = renewal
		}
		return rows.Err()
	})
	if err != nil {
		return nil, errors.New("Saved account renewals could not be read.")
	}
	return latest, nil
}

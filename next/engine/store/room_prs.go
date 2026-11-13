package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/resident-labs/dyna/contract"
	"time"
)

type RoomPRWatch struct {
	Room     contract.RoomID
	Repo     contract.RepoName
	Rows     map[int]TrackedPR
	CensusAt int64 // Unix milliseconds; zero means the initial census is pending.
}

type RoomPRPass struct {
	Room           contract.RoomID
	Repo           contract.RepoName
	Rows           []TrackedPR
	Changes        []contract.PrChange
	CensusComplete bool // This pass completed the initial census, including an empty one.
}

func (s *Store) RoomPRWatches(ctx context.Context, repo contract.RepoName) ([]RoomPRWatch, error) {
	watches, err := queryAll(ctx, s.Read(), func(r scanner) (RoomPRWatch, error) {
		var w RoomPRWatch
		err := r.Scan(&w.Room, &w.Repo, &w.CensusAt)
		w.Rows = map[int]TrackedPR{}
		return w, err
	}, `SELECT room,repo,pr_census_at FROM room_repos WHERE repo=? AND pr_events=1`, repo)
	if err != nil {
		return nil, err
	}
	for i := range watches {
		rows, err := queryAll(ctx, s.Read(), func(r scanner) (TrackedPR, error) {
			var row TrackedPR
			var data string
			if err := r.Scan(&data); err != nil {
				return row, err
			}
			err := json.Unmarshal([]byte(data), &row)
			return row, err
		}, `SELECT snapshot FROM room_pr_observed WHERE room=? AND repo=?`, watches[i].Room, repo)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			watches[i].Rows[row.Number] = row
		}
	}
	return watches, nil
}

func (s *Store) applyRoomPRs(ctx context.Context, tx *sql.Tx, passes []RoomPRPass, at time.Time) error {
	for _, pass := range passes {
		var enabled bool
		err := tx.QueryRowContext(ctx, `SELECT pr_events FROM room_repos WHERE room=? AND repo=?`, pass.Room, pass.Repo).Scan(&enabled)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if !enabled {
			continue
		}
		if pass.CensusComplete {
			if _, err := tx.ExecContext(ctx, `UPDATE room_repos SET pr_census_at=? WHERE room=? AND repo=? AND pr_census_at=0`, at.UnixMilli(), pass.Room, pass.Repo); err != nil {
				return err
			}
		}
		for _, row := range pass.Rows {
			data, err := json.Marshal(row)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO room_pr_observed(room,repo,number,snapshot) VALUES(?,?,?,?) ON CONFLICT(room,repo,number) DO UPDATE SET snapshot=excluded.snapshot`, pass.Room, pass.Repo, row.Number, string(data)); err != nil {
				return err
			}
		}
		if len(pass.Changes) > 0 {
			if _, err := s.appendEngineEvent(tx, pass.Room, at, contract.PrChanged{Changes: pass.Changes}); err != nil {
				return err
			}
			for _, change := range pass.Changes {
				for _, event := range prEvents(change) {
					// Action feeds already publish untracked close and merge events.
					// CI has no feed, so its settled result needs the detail-read event.
					if _, ci := event.(contract.CIFinished); !ci {
						continue
					}
					if _, err := s.appendEngineEvent(tx, pass.Room, at, event); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ErrPRTracked means another session already tracks this open PR.
var ErrPRTracked = errors.New("another session tracks this pull request")

// PRCursors are the newest GitHub IDs seen. Reviews, inline comments and conversation comments number separately.
type PRCursors struct {
	Reviews, ReviewComments, Comments int64
}

// TrackedPR is one pr_owners row. Baseline means the next poll records the PR without a wake.
// OwnWakeAt is when a comment by the token's own account last woke a session, in Unix milliseconds.
type TrackedPR struct {
	contract.PrSummary
	Room      contract.RoomID
	HeadRepo  contract.RepoName
	Baseline  bool
	CITold    string
	Seen      PRCursors
	OwnWakeAt int64
}

// PRUpdate is a poll's result for one PR. A nil Change writes the row and tells nobody.
type PRUpdate struct {
	PR        contract.PrSummary
	HeadRepo  contract.RepoName
	CITold    string
	Seen      PRCursors
	OwnWakeAt int64
	Change    *contract.PrChange
}

// RepoHead is a repository's default branch head.
type RepoHead struct {
	Repo contract.RepoName
	Head string
}

const prColumns = `p.repo,p.number,p.url,p.title,p.author,p.session,p.draft,p.state,p.head,p.review,p.ci,p.merge,s.room,p.head_repo,p.baseline,p.ci_told,p.seen_reviews,p.seen_review_comments,p.seen_comments,p.own_wake_at FROM pr_owners p JOIN sessions s ON s.id=p.session`

func scanPR(row interface{ Scan(...any) error }) (TrackedPR, error) {
	var p TrackedPR
	err := row.Scan(&p.Repo, &p.Number, &p.URL, &p.Title, &p.Author, &p.Session, &p.Draft, &p.State, &p.Head, &p.Review, &p.CI, &p.Merge, &p.Room, &p.HeadRepo, &p.Baseline, &p.CITold, &p.Seen.Reviews, &p.Seen.ReviewComments, &p.Seen.Comments, &p.OwnWakeAt)
	return p, err
}

func (s *Store) queryPRs(ctx context.Context, query string, args ...any) ([]TrackedPR, error) {
	rows, err := s.Read().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackedPR
	for rows.Next() {
		p, err := scanPR(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RepoHeads lists every repository a room lists, with its stored head, empty until a poll reads it.
func (s *Store) RepoHeads(ctx context.Context) (map[contract.RepoName]string, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT repo,main_head FROM repos")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	heads := map[contract.RepoName]string{}
	for rows.Next() {
		var repo contract.RepoName
		var head string
		if err := rows.Scan(&repo, &head); err != nil {
			return nil, err
		}
		heads[repo] = head
	}
	return heads, rows.Err()
}

// TrackedPRs lists every PR the poll reads.
func (s *Store) TrackedPRs(ctx context.Context) ([]TrackedPR, error) {
	return s.queryPRs(ctx, `SELECT `+prColumns+` WHERE p.state='open' ORDER BY p.repo,p.number`)
}

// RoomPRs lists a room's PRs, or every room's when room is empty. all adds merged and closed ones, newest numbers first.
func (s *Store) RoomPRs(ctx context.Context, room contract.RoomID, all bool) ([]TrackedPR, error) {
	return s.queryPRs(ctx, `SELECT `+prColumns+` WHERE (?1='' OR s.room=?1) AND (?2 OR p.state='open') ORDER BY p.state='open' DESC,p.repo,p.number DESC LIMIT 500`, room, all)
}

// PR reads one tracked PR.
func (s *Store) PR(ctx context.Context, repo contract.RepoName, number int) (TrackedPR, error) {
	return scanPR(s.Read().QueryRowContext(ctx, `SELECT `+prColumns+` WHERE p.repo=? AND p.number=?`, repo, number))
}

// RecordPROpen makes session the owner of a PR and appends its event and deliveries in one transaction.
// The goal and approved head start empty. A new PR has no baseline: everything the poll sees on it is news.
func (s *Store) RecordPROpen(ctx context.Context, repo contract.RepoName, number int, session contract.SessionID, branch, title, url string, event contract.Event, deliveries []contract.Delivery) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "WITH RECURSIVE ancestors(id,parent,closed_at,depth) AS (SELECT id,parent,closed_at,0 FROM sessions WHERE id=? UNION ALL SELECT s.id,s.parent,s.closed_at,a.depth+1 FROM sessions s JOIN ancestors a ON s.id=a.parent) INSERT INTO pr_owners(repo,number,session,goal,approved_head,branch,head_repo,title,url,baseline) SELECT ?,?,id,'','',?,?,?,?,0 FROM ancestors WHERE closed_at IS NULL ORDER BY depth LIMIT 1", session, repo, number, branch, repo, title, url)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return sql.ErrNoRows
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		var recipient contract.SessionID
		err = tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id,parent,closed_at,depth) AS (
			SELECT p.id,p.parent,p.closed_at,1 FROM sessions s JOIN sessions p ON p.id=s.parent WHERE s.id=?
			UNION ALL SELECT p.id,p.parent,p.closed_at,a.depth+1 FROM sessions p JOIN ancestors a ON p.id=a.parent
		) SELECT id FROM ancestors WHERE closed_at IS NULL ORDER BY depth LIMIT 1`, session).Scan(&recipient)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if recipient != "" {
			deliveries = append(deliveries, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: recipient}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true})
		}
		for _, delivery := range deliveries {
			if err := s.InsertDelivery(ctx, tx, seq, event.Body, delivery); err != nil {
				return err
			}
		}
		return nil
	})
}

// TrackPR adds a PR with a baseline, so what it already shows wakes nobody. It records no branch, because a branch
// in pr_owners is a push right (OwnsPRBranch) and tracking is only reading. A merged or closed row can be tracked again.
func (s *Store) TrackPR(ctx context.Context, pr TrackedPR) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `INSERT INTO pr_owners(repo,number,session,goal,approved_head,head_repo,url,title,author,draft,state,head) VALUES(?,?,?,'','',?,?,?,?,?,?,?)
ON CONFLICT(repo,number) DO UPDATE SET branch=CASE WHEN session=excluded.session THEN branch ELSE '' END,session=excluded.session,approved_head='',
head_repo=excluded.head_repo,url=excluded.url,title=excluded.title,author=excluded.author,draft=excluded.draft,state=excluded.state,head=excluded.head,
review='',ci='',merge='',ci_told='',seen_reviews=0,seen_review_comments=0,seen_comments=0,own_wake_at=0,baseline=1 WHERE state<>'open'`,
			pr.Repo, pr.Number, pr.Session, pr.HeadRepo, pr.URL, pr.Title, pr.Author, pr.Draft, pr.State, pr.Head)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 1 {
			return err
		}
		var owner contract.SessionID
		if err := tx.QueryRowContext(ctx, "SELECT session FROM pr_owners WHERE repo=? AND number=?", pr.Repo, pr.Number).Scan(&owner); err != nil {
			return err
		}
		if owner != pr.Session {
			return ErrPRTracked
		}
		return nil
	})
}

// UntrackPR stops tracking a PR.
func (s *Store) UntrackPR(ctx context.Context, repo contract.RepoName, number int) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM pr_owners WHERE repo=? AND number=?", repo, number)
		return err
	})
}

// prRecipient finds who hears about a PR: its owner, or the nearest open ancestor if the owner has closed.
const prRecipient = `WITH RECURSIVE ancestors(id,parent,closed_at,room,depth) AS (
	SELECT s.id,s.parent,s.closed_at,s.room,0 FROM pr_owners p JOIN sessions s ON s.id=p.session WHERE p.repo=? AND p.number=?
	UNION ALL SELECT s.id,s.parent,s.closed_at,s.room,a.depth+1 FROM sessions s JOIN ancestors a ON s.id=a.parent
) SELECT id,room FROM ancestors WHERE closed_at IS NULL ORDER BY depth LIMIT 1`

// prEvents are what a change states to subscribers. The pr.changed beside them is what tells the owner, so they stay undelivered here.
func prEvents(change contract.PrChange) []contract.EventBody {
	var bodies []contract.EventBody
	pr := change.PR
	for _, kind := range change.Kinds {
		switch kind {
		case contract.PrChangeMerged:
			bodies = append(bodies, contract.PrMergedEvent{Repo: pr.Repo, Number: pr.Number, Session: pr.Session, URL: pr.URL})
		case contract.PrChangeClosed:
			bodies = append(bodies, contract.PrClosedEvent{Repo: pr.Repo, Number: pr.Number, Session: pr.Session, URL: pr.URL})
		case contract.PrChangeCIFailed, contract.PrChangeCIPassed:
			bodies = append(bodies, contract.CIFinished{Repo: pr.Repo, Number: pr.Number, Session: pr.Session, URL: pr.URL, Head: pr.Head, Result: pr.CI})
		}
	}
	return bodies
}

// appendEngineEvent adds an event from the engine.
func (s *Store) appendEngineEvent(tx *sql.Tx, room contract.RoomID, at time.Time, body contract.EventBody) (int64, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return 0, err
	}
	return s.AppendEvent(tx, contract.Event{ID: contract.EventID("ev_" + hex.EncodeToString(id[:])), Room: room, Time: at, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body})
}

// moveRepoHead stores a head a poll read. The first head ever read is no move. A move is one event in the repository's
// default room, where the default subscription wakes root, and one quiet delivery to the root of each other room that
// lists the repository (spec 7.6). A repository no room lists has no row and no one to tell.
func (s *Store) moveRepoHead(ctx context.Context, tx *sql.Tx, head RepoHead, at time.Time) error {
	var previous string
	var room contract.RoomID
	err := tx.QueryRowContext(ctx, "SELECT main_head,default_room FROM repos WHERE repo=?", head.Repo).Scan(&previous, &room)
	if errors.Is(err, sql.ErrNoRows) || err == nil && previous == head.Head {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE repos SET main_head=? WHERE repo=?", head.Head, head.Repo); err != nil {
		return err
	}
	if previous == "" {
		return nil
	}
	body := contract.RepoMainUpdated{Repo: head.Repo, Head: head.Head}
	seq, err := s.appendEngineEvent(tx, room, at, body)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT s.id FROM room_repos r JOIN sessions s ON s.room=r.room WHERE r.repo=? AND r.room<>? AND s.parent IS NULL AND s.closed_at IS NULL", head.Repo, room)
	if err != nil {
		return err
	}
	var roots []contract.SessionID
	for rows.Next() {
		var root contract.SessionID
		if err := rows.Scan(&root); err != nil {
			rows.Close()
			return err
		}
		roots = append(roots, root)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, root := range roots {
		if err := s.InsertDelivery(ctx, tx, seq, body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: root}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}}); err != nil {
			return err
		}
	}
	return nil
}

// ApplyPRPoll writes one pass of the poll and its events in one transaction, so a restart can neither lose a wake nor
// repeat one. Each recipient gets one event for the pass. A row that went away meanwhile drops out.
func (s *Store) ApplyPRPoll(ctx context.Context, updates []PRUpdate, heads []RepoHead, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		type batch struct {
			session contract.SessionID
			room    contract.RoomID
			changes []contract.PrChange
		}
		var batches []*batch
		bySession := map[contract.SessionID]*batch{}
		for _, update := range updates {
			pr := update.PR
			result, err := tx.ExecContext(ctx, `UPDATE pr_owners SET head_repo=?,url=?,title=?,author=?,draft=?,state=?,head=?,review=?,ci=?,merge=?,ci_told=?,seen_reviews=?,seen_review_comments=?,seen_comments=?,own_wake_at=?,baseline=0 WHERE repo=? AND number=? AND state='open'`,
				update.HeadRepo, pr.URL, pr.Title, pr.Author, pr.Draft, pr.State, pr.Head, pr.Review, pr.CI, pr.Merge, update.CITold, update.Seen.Reviews, update.Seen.ReviewComments, update.Seen.Comments, update.OwnWakeAt, pr.Repo, pr.Number)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 || update.Change == nil {
				continue
			}
			var session contract.SessionID
			var room contract.RoomID
			err = tx.QueryRowContext(ctx, prRecipient, pr.Repo, pr.Number).Scan(&session, &room)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			group := bySession[session]
			if group == nil {
				group = &batch{session: session, room: room}
				bySession[session] = group
				batches = append(batches, group)
			}
			change := *update.Change
			change.PR.Session = session
			group.changes = append(group.changes, change)
		}
		for _, group := range batches {
			body := contract.PrChanged{Session: group.session, Changes: group.changes}
			seq, err := s.appendEngineEvent(tx, group.room, at, body)
			if err != nil {
				return err
			}
			if body.Waking() {
				if err := s.InsertDelivery(ctx, tx, seq, body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: group.session}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}); err != nil {
					return err
				}
			}
			for _, change := range group.changes {
				for _, event := range prEvents(change) {
					if _, err := s.appendEngineEvent(tx, group.room, at, event); err != nil {
						return err
					}
				}
			}
		}
		for _, head := range heads {
			if err := s.moveRepoHead(ctx, tx, head, at); err != nil {
				return err
			}
		}
		return nil
	})
}

// OwnsPRBranch reports whether a session owns a PR on this branch.
func (s *Store) OwnsPRBranch(ctx context.Context, repo contract.RepoName, session contract.SessionID, branch string) (bool, error) {
	var found bool
	err := s.Read().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pr_owners WHERE repo=? AND session=? AND branch=?)", repo, session, branch).Scan(&found)
	return found, err
}

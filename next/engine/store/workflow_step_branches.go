package store

import (
	"context"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type WorkflowStepBranch struct {
	Room   contract.RoomID
	Repo   contract.RepoName
	Branch string
	Commit string
}

// ExpiredWorkflowStepBranches finds published step refs whose journal copies are all old. Resumes copy results verbatim,
// so a ref needs one producing run (whose branch supplies the name), but any copy can keep it alive.
func (s *Store) ExpiredWorkflowStepBranches(ctx context.Context, cutoff time.Time) ([]WorkflowStepBranch, error) {
	rows, err := s.Read().QueryContext(ctx, `WITH results AS MATERIALIZED (
    SELECT w.room, w.repo, w.session, w.state, w.ended_at, substr(j.key,1,12) AS suffix,
           json_extract(j.result,'$.branch') AS branch, json_extract(j.result,'$.commit') AS commit_sha
      FROM workflow_steps j JOIN workflow_runs w ON w.id=j.run
     WHERE j.kind=? AND j.state=? AND w.repo!='' AND length(j.key)>=12
       AND substr(j.key,1,12) NOT GLOB '*[^0-9a-f]*'
       AND CASE WHEN json_valid(j.result) THEN json_type(j.result,'$.branch')='text'
           AND json_type(j.result,'$.commit')='text' AND coalesce(json_extract(j.result,'$.unpublished'),0)=0 END
), candidates AS (
    SELECT repo, branch, min(commit_sha) AS commit_sha,
           min(CASE WHEN EXISTS (SELECT 1 FROM session_branches b
                WHERE b.session=r.session AND b.repo=r.repo COLLATE NOCASE AND r.branch=b.branch||'-'||r.suffix)
             AND EXISTS (SELECT 1 FROM room_repos a WHERE a.room=r.room AND a.repo=r.repo COLLATE NOCASE)
             THEN room END) AS room
      FROM results r GROUP BY repo COLLATE NOCASE, branch
    HAVING max(state=? OR ended_at=0 OR ended_at>?)=0 AND min(commit_sha)=max(commit_sha)
)
SELECT room,repo,branch,commit_sha FROM candidates c WHERE room IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM session_branches b WHERE b.repo=c.repo COLLATE NOCASE AND b.branch=c.branch)
 ORDER BY room, repo COLLATE NOCASE, branch`, contract.WorkflowStepAgent, contract.WorkflowStepDone, contract.WorkflowRunning, cutoff.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var branches []WorkflowStepBranch
	for rows.Next() {
		var branch WorkflowStepBranch
		if err := rows.Scan(&branch.Room, &branch.Repo, &branch.Branch, &branch.Commit); err != nil {
			return nil, err
		}
		branches = append(branches, branch)
	}
	return branches, rows.Err()
}

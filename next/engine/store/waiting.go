package store

import (
	"context"
	"github.com/resident-labs/dyna/contract"
)

// sessionWaits reads all facts in one query for a room. It propagates actual
// running turns, not waiting labels, so waiting chains never sustain themselves.
// A workflow's crew is represented by its run, not as an ordinary child turn.
func sessionWaits(ctx context.Context, q sqlQuery, room contract.RoomID) (map[contract.SessionID]*contract.SessionWaiting, error) {
	rows, err := q.QueryContext(ctx, `SELECT s.id,COALESCE(s.parent,''),s.name,s.shell_jobs,
 EXISTS(SELECT 1 FROM turns t WHERE t.session=s.id AND t.ended_at IS NULL) AND COALESCE(b.state,'') NOT IN ('failed','lost'),
 (SELECT COUNT(*) FROM questions WHERE session=s.id AND state='pending')+(SELECT COUNT(*) FROM operations WHERE by_kind='session' AND by_id=s.id AND class='owner' AND state='pending'),
 COALESCE(w.name,''),COALESCE(w.state,'')='running', w.id IS NOT NULL
 FROM sessions s LEFT JOIN boxes b ON b.session=s.id LEFT JOIN workflow_runs w ON w.session=s.id WHERE s.room=? AND s.closed_at IS NULL ORDER BY s.created_at,s.id`, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type fact struct {
		id, parent            contract.SessionID
		name                  string
		jobs, owner           int
		running               bool
		workflow              string
		workflowRunning, crew bool
	}
	var facts []fact
	byID := map[contract.SessionID]fact{}
	for rows.Next() {
		var f fact
		if err = rows.Scan(&f.id, &f.parent, &f.name, &f.jobs, &f.running, &f.owner, &f.workflow, &f.workflowRunning, &f.crew); err != nil {
			return nil, err
		}
		facts = append(facts, f)
		byID[f.id] = f
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	activeChildren := map[contract.SessionID]map[contract.SessionID]string{}
	workflows := map[contract.SessionID]*contract.SessionWaiting{}
	for _, f := range facts {
		if f.crew && f.workflowRunning {
			w := workflows[f.parent]
			if w == nil {
				w = &contract.SessionWaiting{Kind: contract.WaitWorkflow, Name: f.workflow}
				workflows[f.parent] = w
			}
			w.Count++
		}
		if !f.running || f.crew {
			continue
		}
		for child := f; child.parent != ""; child = byID[child.parent] {
			if activeChildren[child.parent] == nil {
				activeChildren[child.parent] = map[contract.SessionID]string{}
			}
			activeChildren[child.parent][child.id] = child.name
		}
	}
	out := map[contract.SessionID]*contract.SessionWaiting{}
	for _, f := range facts {
		if f.running {
			continue
		}
		switch {
		case f.owner > 0:
			out[f.id] = &contract.SessionWaiting{Kind: contract.WaitOwner, Count: f.owner}
		case f.jobs > 0:
			out[f.id] = &contract.SessionWaiting{Kind: contract.WaitJobs, Count: f.jobs}
		case len(activeChildren[f.id]) > 0:
			children := activeChildren[f.id]
			var name string
			for _, n := range children {
				name = n
				break
			}
			if len(children) > 1 {
				name = ""
			}
			out[f.id] = &contract.SessionWaiting{Kind: contract.WaitChildren, Count: len(children), Name: name}
		case workflows[f.id] != nil:
			out[f.id] = workflows[f.id]
		}
	}
	return out, nil
}

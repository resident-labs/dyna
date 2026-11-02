package store

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"

	"github.com/resident-labs/dyna/contract"
)

// This file is the one answer to "what does this session still hold?". Box stop, the 7-day close, disk eviction, check-ins,
// close, the agent's context and the owner's About panel all read this list, and none of them keeps a rule of its own about
// what holds a session.

type responsibilityRule struct {
	kind  contract.ResponsibilityKind
	group contract.ResponsibilityGroup
	// sevenDay keeps the session from the 7-day close.
	sevenDay bool
	// eviction keeps it from disk eviction, which skips that wait: close archives the work and hands it on.
	eviction bool
	// handed moves it to the parent on close. Anything else ends with the session.
	handed bool
}

// responsibilityRules says what each kind is and what it does. A session's list comes back in this order.
var responsibilityRules = []responsibilityRule{
	// Needs the box running. Each of these runs in the box, so nothing stops or closes it unasked.
	{kind: contract.ResponsibilityTurn, group: contract.ResponsibilityNeedsBox, sevenDay: true, eviction: true},
	{kind: contract.ResponsibilityShellJob, group: contract.ResponsibilityNeedsBox, sevenDay: true, eviction: true},
	{kind: contract.ResponsibilityTerminal, group: contract.ResponsibilityNeedsBox, sevenDay: true, eviction: true},
	{kind: contract.ResponsibilityWorkflow, group: contract.ResponsibilityNeedsBox, sevenDay: true, eviction: true},
	// Owes work. A box may stop under all of these: the work waits for a wake, and it survives the box.
	{kind: contract.ResponsibilityGoal, group: contract.ResponsibilityOwesWork, sevenDay: true, handed: true},
	{kind: contract.ResponsibilityPR, group: contract.ResponsibilityOwesWork, sevenDay: true, handed: true},
	// An issue can stay open for months, so it holds nothing and close simply hands it on.
	{kind: contract.ResponsibilityIssue, group: contract.ResponsibilityOwesWork, handed: true},
	// An answer needs the session that asked, so close withdraws a question rather than moving it.
	{kind: contract.ResponsibilityQuestion, group: contract.ResponsibilityOwesWork, sevenDay: true, eviction: true},
	// A close takes the subtree along, deepest first.
	{kind: contract.ResponsibilityChild, group: contract.ResponsibilityOwesWork, sevenDay: true, eviction: true},
}

// responsibilityQuery reads the lists of a JSON array of sessions in one statement. Each branch walks an index by session.
//
// sessions.shell_jobs counts a session's shell jobs and the last turn.ended names them. A job that ended since is in
// shell_jobs. A box agent too old to name its jobs leaves only the count, which reads as one unnamed job.
//
// A run needs its turn: a crew whose turn failed waits for a restart, and its box does nothing until then.
const responsibilityQuery = `WITH wanted(id) AS (SELECT value FROM json_each(?)),
jobs(session,ids) AS (SELECT s.id,(SELECT json_extract(CAST(e.body AS TEXT),'$.jobs') FROM events e WHERE e.room=s.room AND e.from_kind='session' AND e.from_id=s.id AND e.type='turn.ended' ORDER BY e.seq DESC LIMIT 1) FROM sessions s JOIN wanted w ON w.id=s.id WHERE s.shell_jobs>0)
SELECT session,kind,id,blocked FROM (
	SELECT t.session session,'turn' kind,t.id id,0 blocked FROM turns t JOIN wanted w ON w.id=t.session WHERE t.ended_at IS NULL
	UNION ALL SELECT j.session,'shell_job',e.value,0 FROM jobs j,json_each(j.ids) e WHERE NOT EXISTS (SELECT 1 FROM shell_jobs x WHERE x.session=j.session AND x.job=e.value)
	UNION ALL SELECT session,'shell_job','',0 FROM jobs WHERE ids IS NULL
	UNION ALL SELECT r.session,'workflow',r.id,0 FROM workflow_runs r JOIN wanted w ON w.id=r.session JOIN turns t ON t.session=r.session AND t.ended_at IS NULL WHERE r.state='running'
	UNION ALL SELECT g.session,'goal','g'||g.number,0 FROM goals g JOIN wanted w ON w.id=g.session WHERE g.state='active'
	UNION ALL SELECT p.session,'pr',p.repo||'#'||p.number,0 FROM pr_owners p JOIN wanted w ON w.id=p.session WHERE p.state='open'
	UNION ALL SELECT i.session,'issue',i.repo||'#'||i.number,0 FROM issue_owners i JOIN wanted w ON w.id=i.session WHERE i.state='open'
	UNION ALL SELECT q.session,'question',q.id,q.blocked FROM questions q JOIN wanted w ON w.id=q.session WHERE q.state='pending'
	UNION ALL SELECT c.parent,'child',c.name,0 FROM sessions c JOIN wanted w ON w.id=c.parent WHERE c.closed_at IS NULL
) ORDER BY length(id),id`

// Held is what one session still holds, in the order of responsibilityRules.
type Held []contract.Responsibility

// NeedsBox answers "does anything need this box running?".
func (h Held) NeedsBox() bool {
	return slices.ContainsFunc(h, func(item contract.Responsibility) bool { return item.Group == contract.ResponsibilityNeedsBox })
}

// Count says how many items of a kind the session holds.
func (h Held) Count(kind contract.ResponsibilityKind) int {
	n := 0
	for _, item := range h {
		if item.Kind == kind {
			n++
		}
	}
	return n
}

// Blocked says an open question left the session nothing safe to do until the owner answers.
func (h Held) Blocked() bool {
	return slices.ContainsFunc(h, func(item contract.Responsibility) bool { return item.Blocked })
}

// holdsSevenDay keeps a session from the 7-day close.
func (h Held) holdsSevenDay() bool {
	return slices.ContainsFunc(h, func(item contract.Responsibility) bool { return ruleOf(item.Kind).sevenDay })
}

// holdsEviction keeps a session from disk eviction.
func (h Held) holdsEviction() bool {
	return slices.ContainsFunc(h, func(item contract.Responsibility) bool { return ruleOf(item.Kind).eviction })
}

// splitClose sorts the list into what close hands to the parent and what ends with the session.
func (h Held) splitClose() (handed, ended []contract.Responsibility) {
	for _, item := range h {
		if ruleOf(item.Kind).handed {
			handed = append(handed, item)
		} else {
			ended = append(ended, item)
		}
	}
	return handed, ended
}

func rank(kind contract.ResponsibilityKind) int {
	return slices.IndexFunc(responsibilityRules, func(rule responsibilityRule) bool { return rule.kind == kind })
}

func ruleOf(kind contract.ResponsibilityKind) responsibilityRule {
	return responsibilityRules[rank(kind)]
}

// Responsibilities lists what one session still holds.
func (s *Store) Responsibilities(ctx context.Context, id contract.SessionID) (Held, error) {
	all, err := s.ResponsibilitiesOf(ctx, []contract.SessionID{id})
	return all[id], err
}

// ResponsibilitiesOf lists what each of many sessions still holds, in one query however many there are.
func (s *Store) ResponsibilitiesOf(ctx context.Context, ids []contract.SessionID) (map[contract.SessionID]Held, error) {
	return s.responsibilities(ctx, s.Read(), ids, s.openTerminals(ids))
}

// SetTerminals gives the list what the store cannot see: an open terminal lives in memory, with the connection that holds it.
func (s *Store) SetTerminals(open func(contract.SessionID) bool) { s.terminals.Store(&open) }

// openTerminals reads which of these sessions have a terminal open. That sits behind the session's lock, and a lock holder
// may be waiting on the writer, so a write reads this first and never inside its transaction.
func (s *Store) openTerminals(ids []contract.SessionID) map[contract.SessionID]bool {
	open := make(map[contract.SessionID]bool)
	if read := s.terminals.Load(); read != nil {
		for _, id := range ids {
			if (*read)(id) {
				open[id] = true
			}
		}
	}
	return open
}

// responsibilities takes the query surface and the terminals, so a write can recheck the list inside its own transaction.
func (s *Store) responsibilities(ctx context.Context, q sqlQuery, ids []contract.SessionID, terminals map[contract.SessionID]bool) (map[contract.SessionID]Held, error) {
	held := make(map[contract.SessionID]Held, len(ids))
	if len(ids) == 0 {
		return held, nil
	}
	wanted, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, responsibilityQuery, string(wanted))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var session contract.SessionID
		var item contract.Responsibility
		if err := rows.Scan(&session, &item.Kind, &item.ID, &item.Blocked); err != nil {
			return nil, err
		}
		held[session] = append(held[session], item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for id := range terminals {
		held[id] = append(held[id], contract.Responsibility{Kind: contract.ResponsibilityTerminal})
	}
	for _, items := range held {
		for i := range items {
			items[i].Group = ruleOf(items[i].Kind).group
		}
		slices.SortStableFunc(items, func(a, b contract.Responsibility) int { return cmp.Compare(rank(a.Kind), rank(b.Kind)) })
	}
	return held, nil
}

// ClosePlan says what closing these sessions does with what they hold. order is the closing session and its subtree, and to
// is the parent that takes the work.
func (s *Store) ClosePlan(ctx context.Context, order []contract.SessionID, to contract.SessionID) (contract.SessionCloseResponse, error) {
	plan := contract.SessionCloseResponse{To: to, Handed: []contract.Responsibility{}, Ended: []contract.Responsibility{}}
	held, err := s.ResponsibilitiesOf(ctx, order)
	if err != nil {
		return plan, err
	}
	for _, id := range order {
		handed, ended := held[id].splitClose()
		plan.Handed, plan.Ended = append(plan.Handed, handed...), append(plan.Ended, ended...)
	}
	return plan, nil
}

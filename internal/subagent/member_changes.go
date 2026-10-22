package subagent

import (
	"path/filepath"
	"strconv"
	"strings"

	"dyna-agent/internal/database"
)

// MemberChanges names sessions written after since. The returned
// revision is captured before reading IDs: a concurrent newer write can be
// read early, but it cannot be hidden by advancing coverage past that read.
// A deletion or counter reset requires a full roster, since rows no longer
// contain the removed IDs. The token is the existing MembersRevision token.
func (s *Store) MemberChanges(since string) (revision string, ids []string, reset bool, err error) {
	revision, err = s.MembersRevision()
	if err != nil || revision == since {
		return
	}
	previous, valid := parseMembersRevision(since)
	current, currentValid := parseMembersRevision(revision)
	if !valid || !currentValid || current.deletes != previous.deletes || current.records < previous.records || current.turns < previous.turns || current.queues < previous.queues || current.count < previous.count || current.records == previous.records && current.newest != previous.newest {
		return revision, nil, true, nil
	}
	db, err := database.Open(filepath.Dir(s.root()))
	if err != nil {
		return revision, nil, false, err
	}
	rows, err := db.Query(memberChangesQuery, previous.records, previous.turns, previous.queues)
	if err != nil {
		return revision, nil, false, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return revision, nil, false, err
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return revision, ids, false, rows.Err()
}

// Match ListSnapshot(true): canonical records include closed sessions.
// A reset still uses the existing listing, including its folder checks.
// UNION would sort the sessions and scan every turn row; UNION ALL lets each
// branch seek its revision index. The caller drops duplicate IDs.
const memberChangesQuery = `SELECT id FROM session_records WHERE revision > ?
UNION ALL SELECT t.session FROM session_turns t JOIN session_records r ON r.id = t.session WHERE t.revision > ?
UNION ALL SELECT q.session FROM session_queues q JOIN session_records r ON r.id = q.session WHERE q.revision > ?`

type membersCursor struct {
	newest                                 string
	records, count, turns, queues, deletes int64
}

func parseMembersRevision(token string) (c membersCursor, valid bool) {
	parts := strings.Split(token, "|")
	if len(parts) != 4 {
		return
	}
	record := strings.Split(parts[0], "/")
	if parts[0] == "" {
		record = []string{"", "0", "0"}
	}
	if len(record) != 3 {
		return
	}
	c.newest = record[0]
	values := []string{record[1], record[2]}
	for i, prefix := range []string{"t", "q", "d"} {
		if !strings.HasPrefix(parts[i+1], prefix) {
			return c, false
		}
		values = append(values, strings.TrimPrefix(parts[i+1], prefix))
	}
	for i, dest := range []*int64{&c.records, &c.count, &c.turns, &c.queues, &c.deletes} {
		n, err := strconv.ParseInt(values[i], 10, 64)
		if err != nil || n < 0 {
			return c, false
		}
		*dest = n
	}
	return c, true
}

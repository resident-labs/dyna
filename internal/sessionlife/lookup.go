package sessionlife

import "context"

// effectiveName is the SQL expression of the name a session answers to: its
// stored name, or the generated name of a session that has none. It matches
// Meta.EffectiveName in subagent, and session_records_effective_name indexes
// it, so the generated name is one seek too.
// TODO: legacy/shim remove later — drop the generated branch, and the two
// parts of the index that serve it, once every retained session has a name
const effectiveName = `CASE WHEN name <> '' THEN name ELSE 'agent-' || substr(id, -8) END`

// Lookup returns the IDs of the session records that answer to name, newest
// created first, whether the name is stored or generated for an unnamed
// session. A non-empty room limits the answer to that room, so one call looks
// up a bare name or a full address.
//
// It is one indexed query over session_records. It reads no session folder
// and no legacy file, so its cost does not grow with the sessions that do not
// match. Callers decide which answers are still eligible.
func Lookup(sessions, room, name string) ([]string, error) {
	if name == "" {
		return nil, nil
	}
	db, ok, err := readable(sessions)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := db.QueryContext(context.Background(), lookupQuery(room != ""), lookupArgs(room, name)...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	return ids, rows.Close()
}

// lookupQuery seeks the requested name in session_records_effective_name. It
// is the same equality for a stored name and a generated one, so neither kind
// reads the rows around it.
func lookupQuery(room bool) string {
	roomFilter := ""
	if room {
		roomFilter = " AND room = ?"
	}
	return "SELECT id FROM session_records WHERE " + effectiveName + " = ?" + roomFilter +
		" ORDER BY created_ns DESC, id"
}

// lookupArgs are the arguments lookupQuery takes, in its placeholder order.
func lookupArgs(room, name string) []any {
	args := []any{name}
	if room != "" {
		args = append(args, room)
	}
	return args
}

package sessionlife

import "context"

// generatedName is the name a session without a stored name answers to, in the
// SQL of the record table. It matches Meta.EffectiveName in subagent.
// TODO: legacy/shim remove later — drop the unnamed fallback, and the
// session_records rows it serves, once every retained session has a name
const generatedName = "'agent-' || substr(id, -8)"

// Lookup returns the IDs of the session records that answer to name, newest
// created first: the sessions stored with that name, and the unnamed ones
// whose generated name is name. A non-empty room limits the answer to that
// room, so one call looks up a bare name or a full address.
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

// lookupQuery answers a name lookup from session_records_name: the stored
// names, and the generated names of unnamed sessions, in one statement.
func lookupQuery(room bool) string {
	roomFilter := ""
	if room {
		roomFilter = " AND room = ?"
	}
	return "SELECT id FROM (" +
		"SELECT id, created_ns FROM session_records WHERE name = ?" + roomFilter +
		" UNION ALL " +
		"SELECT id, created_ns FROM session_records WHERE name = '' AND ? = " + generatedName + roomFilter +
		") ORDER BY created_ns DESC, id"
}

// lookupArgs are the arguments lookupQuery takes, in its placeholder order.
func lookupArgs(room, name string) []any {
	args := []any{name}
	if room != "" {
		args = append(args, room)
	}
	args = append(args, name)
	if room != "" {
		args = append(args, room)
	}
	return args
}

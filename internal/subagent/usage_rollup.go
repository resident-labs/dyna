package subagent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// The usage rollup keeps one row per day, session, profile and model in
// dyna.db. usage_turn holds one key per folded turn, so replaying a turn
// never counts it twice. Unknown values add to the unknown counters only.
const usageRollupSchema = `
CREATE TABLE IF NOT EXISTS usage_turn (
  session_id TEXT NOT NULL, n INTEGER NOT NULL, day TEXT NOT NULL,
  PRIMARY KEY (session_id, n)
);
CREATE TABLE IF NOT EXISTS usage_rollup (
  day TEXT NOT NULL, session_id TEXT NOT NULL, profile TEXT NOT NULL, model TEXT NOT NULL,
  room_id TEXT NOT NULL, role TEXT NOT NULL, harness TEXT NOT NULL,
  turns INTEGER NOT NULL, wall_ms INTEGER NOT NULL,
  input INTEGER NOT NULL, input_complete INTEGER NOT NULL, input_partial INTEGER NOT NULL, input_unknown INTEGER NOT NULL,
  cached INTEGER NOT NULL, cached_complete INTEGER NOT NULL, cached_partial INTEGER NOT NULL, cached_unknown INTEGER NOT NULL,
  output INTEGER NOT NULL, output_complete INTEGER NOT NULL, output_partial INTEGER NOT NULL, output_unknown INTEGER NOT NULL,
  cost_usd REAL NOT NULL, cost_complete INTEGER NOT NULL, cost_partial INTEGER NOT NULL, cost_unknown INTEGER NOT NULL,
  pair_input INTEGER NOT NULL, pair_cached INTEGER NOT NULL, pair_turns INTEGER NOT NULL,
  PRIMARY KEY (day, session_id, profile, model)
);
CREATE INDEX IF NOT EXISTS usage_rollup_room_day ON usage_rollup(room_id, day);`

// usageRollupVersion changes whenever the layout or the accounting rules change,
// so rows folded under older rules are rebuilt from the turn files.
const usageRollupVersion = 3

// RoleSubagent is the rollup role of a session with no stored role.
const RoleSubagent = "subagent"

// Migrate brings the usage rollup tables in dataDir's dyna.db to this version.
// Only the engine calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	s := &Store{Root: filepath.Join(dataDir, "sessions")}
	if _, err := s.usageDB(); err != nil {
		return err
	}
	db, err := s.turnsDB()
	if err != nil {
		return err
	}
	if err := s.migrateRetainedTurns(); err != nil {
		return err
	}
	if err := migrateStoredPrompts(db); err != nil {
		return err
	}
	if err := s.migrateDeliveryReasons(); err != nil {
		return err
	}
	if err := s.migrateTurnCutoffs(); err != nil {
		return err
	}
	if err := s.migrateRetainedUsage(); err != nil {
		return err
	}
	if err := s.migrateOutputProjection(); err != nil {
		return err
	}
	return migrateQueues(dataDir)
}

func (s *Store) usageDB() (*sql.DB, error) {
	db, err := database.Open(filepath.Dir(s.root()))
	if err != nil {
		return nil, err
	}
	err = database.Migrate(db, "usage.rollup.version", usageRollupVersion, func(conn *sql.Conn, _ int) error {
		// An older layout or older accounting rules: the next reconcile
		// refolds every turn file into the new table.
		_, err := conn.ExecContext(context.Background(), `DROP TABLE IF EXISTS usage_rollup; DROP TABLE IF EXISTS usage_turn;`+usageRollupSchema)
		return err
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// usageMetric is one metric of one turn. Only complete and partial reports add
// a value; a missing or negative report counts as unknown.
type usageMetric struct {
	value                      int64
	complete, partial, unknown int
}

func newUsageMetric(value int64, state string) usageMetric {
	switch {
	case value >= 0 && state == "complete":
		return usageMetric{value: value, complete: 1}
	case value >= 0 && state == "partial":
		return usageMetric{value: value, partial: 1}
	}
	return usageMetric{unknown: 1}
}

func turnUsageMetrics(t Turn) (input, cached, output, cost usageMetric, costUSD float64, pair usagePair) {
	n := t.UsageNormalization
	costState := n.Cost
	if t.Usage.CostUSD < 0 {
		costState = ""
	}
	cost = newUsageMetric(0, costState)
	if cost.unknown == 0 {
		costUSD = t.Usage.CostUSD
	}
	input, cached, output = newUsageMetric(t.Usage.InputTokens, n.Input), newUsageMetric(t.Usage.CachedTokens, n.CachedInput), newUsageMetric(t.Usage.OutputTokens, n.Output)
	// Cached input is a subset of input. A larger cache count is a harness
	// accounting error, so it is unknown rather than a value above input.
	if input.complete == 1 && cached.unknown == 0 && cached.value > input.value {
		cached = usageMetric{unknown: 1}
	}
	if input.complete == 1 && cached.complete == 1 {
		pair = usagePair{input: input.value, cached: cached.value, turns: 1}
	}
	return input, cached, output, cost, costUSD, pair
}

// usagePair is input and cache summed over a turn that reported both.
type usagePair struct{ input, cached, turns int64 }

// RecordTurnUsage folds one finished turn into the rollup. It is idempotent:
// a turn that is already folded changes nothing.
func (s *Store) RecordTurnUsage(m Meta, t Turn) error {
	if !t.Done() || m.RoomID == "" {
		return nil
	}
	db, err := s.usageDB()
	if err != nil {
		return err
	}
	at := t.Started
	if at.IsZero() {
		at = t.Created
	}
	day := at.UTC().Format(time.DateOnly)
	var wallMs int64
	if !t.Started.IsZero() && t.Ended.After(t.Started) {
		wallMs = t.Ended.Sub(t.Started).Milliseconds()
	}
	role := m.Role
	if role == "" {
		role = RoleSubagent
	}
	in, cached, out, cost, costUSD, pair := turnUsageMetrics(t)
	return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		res, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO usage_turn(session_id,n,day) VALUES (?,?,?)`, m.ID, t.N, day)
		if err != nil {
			return err
		}
		if added, _ := res.RowsAffected(); added == 0 {
			return nil
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO usage_rollup VALUES (?,?,?,?,?,?,?, 1,?, ?,?,?,?, ?,?,?,?, ?,?,?,?, ?,?,?,?, ?,?,?)
ON CONFLICT(day,session_id,profile,model) DO UPDATE SET
  turns=turns+1, wall_ms=wall_ms+excluded.wall_ms,
  input=input+excluded.input, input_complete=input_complete+excluded.input_complete, input_partial=input_partial+excluded.input_partial, input_unknown=input_unknown+excluded.input_unknown,
  cached=cached+excluded.cached, cached_complete=cached_complete+excluded.cached_complete, cached_partial=cached_partial+excluded.cached_partial, cached_unknown=cached_unknown+excluded.cached_unknown,
  output=output+excluded.output, output_complete=output_complete+excluded.output_complete, output_partial=output_partial+excluded.output_partial, output_unknown=output_unknown+excluded.output_unknown,
  cost_usd=cost_usd+excluded.cost_usd, cost_complete=cost_complete+excluded.cost_complete, cost_partial=cost_partial+excluded.cost_partial, cost_unknown=cost_unknown+excluded.cost_unknown,
  pair_input=pair_input+excluded.pair_input, pair_cached=pair_cached+excluded.pair_cached, pair_turns=pair_turns+excluded.pair_turns`,
			day, m.ID, t.Profile, t.Model, m.RoomID, role, t.Harness, wallMs,
			in.value, in.complete, in.partial, in.unknown,
			cached.value, cached.complete, cached.partial, cached.unknown,
			out.value, out.complete, out.partial, out.unknown,
			costUSD, cost.complete, cost.partial, cost.unknown,
			pair.input, pair.cached, pair.turns)
		return err
	})
}

// ReconcileUsage folds finished turns that have no usage_turn key. It compares
// the folded count of each session with Meta.Turns, so it opens a turn file
// only for a session with a missing turn. The first run is the backfill of
// turns finished before the rollup existed.
func (s *Store) ReconcileUsage(ctx context.Context) error {
	db, err := s.usageDB()
	if err != nil {
		return err
	}
	folded := map[string]int{}
	rows, err := db.QueryContext(ctx, `SELECT session_id, COUNT(*) FROM usage_turn GROUP BY session_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			rows.Close()
			return err
		}
		folded[id] = count
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	ids, err := s.sessionIDs()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		meta, err := s.ReadMeta(id)
		if err != nil || meta.RoomID == "" || folded[meta.ID] >= meta.Turns {
			continue
		}
		have, err := s.foldedTurns(db, meta.ID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for n := 1; n <= meta.Turns; n++ {
			if have[n] {
				continue
			}
			if t, err := s.ReadTurn(meta.ID, n); err == nil {
				if err := s.RecordTurnUsage(meta, t); err != nil {
					failures = append(failures, err)
				}
			}
		}
	}
	return errors.Join(failures...)
}

func (s *Store) foldedTurns(db *sql.DB, id string) (map[int]bool, error) {
	rows, err := db.Query(`SELECT n FROM usage_turn WHERE session_id=?`, id)
	if err != nil {
		return nil, err
	}
	have := map[int]bool{}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		have[n] = true
	}
	return have, errors.Join(rows.Err(), rows.Close())
}

// UsageMetric is a token total with its coverage. Tokens is nil when no turn
// reported the metric, so unknown never reads as zero.
type UsageMetric struct {
	Tokens        *int64 `json:"tokens"`
	CompleteTurns int    `json:"completeTurns"`
	PartialTurns  int    `json:"partialTurns"`
	UnknownTurns  int    `json:"unknownTurns"`
}

// UsageCost is the sum of reported cost. It is a lower bound when turns are
// partial or unknown.
type UsageCost struct {
	USD           *float64 `json:"usd"`
	CompleteTurns int      `json:"completeTurns"`
	PartialTurns  int      `json:"partialTurns"`
	UnknownTurns  int      `json:"unknownTurns"`
}

// UsageRollupRow is one group of the read call. Day, Role and Model name the
// group and are empty in the totals row.
type UsageRollupRow struct {
	Day    string      `json:"day,omitempty"`
	Role   string      `json:"role,omitempty"`
	Model  string      `json:"model,omitempty"`
	Turns  int         `json:"turns"`
	WallMs int64       `json:"wallMs"`
	Input  UsageMetric `json:"input"`
	Cached UsageMetric `json:"cached"`
	Output UsageMetric `json:"output"`
	Cost   UsageCost   `json:"cost"`
	// CachePaired sums input and cached tokens over the turns that reported
	// both, so Cached / Input is a true cache rate for those turns.
	CachePaired UsageCachePaired `json:"cachePaired"`
}

// UsageCachePaired is input and cache summed over the same turns.
type UsageCachePaired struct {
	Input  int64 `json:"input"`
	Cached int64 `json:"cached"`
	Turns  int   `json:"turns"`
}

// UsageRollupSnapshot answers the usage screen from one query.
type UsageRollupSnapshot struct {
	RoomID      string           `json:"roomId"`
	From        string           `json:"from"`
	To          string           `json:"to"`
	Totals      UsageRollupRow   `json:"totals"`
	ByDayModel  []UsageRollupRow `json:"byDayModel"`
	ByRoleModel []UsageRollupRow `json:"byRoleModel"`
}

type usageSums struct {
	turns, wallMs                      int64
	input, cached, output              [4]int64 // sum, complete, partial, unknown
	cost                               float64
	pairInput, pairCached, pairTurns   int64
	costComplete, costPartial, costUnk int64
}

func (a *usageSums) add(b usageSums) {
	a.turns += b.turns
	a.wallMs += b.wallMs
	for i := range a.input {
		a.input[i] += b.input[i]
		a.cached[i] += b.cached[i]
		a.output[i] += b.output[i]
	}
	a.cost += b.cost
	a.pairInput += b.pairInput
	a.pairCached += b.pairCached
	a.pairTurns += b.pairTurns
	a.costComplete += b.costComplete
	a.costPartial += b.costPartial
	a.costUnk += b.costUnk
}

func (a usageSums) row() UsageRollupRow {
	metric := func(v [4]int64) UsageMetric {
		m := UsageMetric{CompleteTurns: int(v[1]), PartialTurns: int(v[2]), UnknownTurns: int(v[3])}
		if v[1]+v[2] > 0 {
			m.Tokens = &v[0]
		}
		return m
	}
	cost := UsageCost{CompleteTurns: int(a.costComplete), PartialTurns: int(a.costPartial), UnknownTurns: int(a.costUnk)}
	if a.costComplete+a.costPartial > 0 {
		cost.USD = &a.cost
	}
	return UsageRollupRow{Turns: int(a.turns), WallMs: a.wallMs, Input: metric(a.input), Cached: metric(a.cached), Output: metric(a.output), Cost: cost, CachePaired: UsageCachePaired{Input: a.pairInput, Cached: a.pairCached, Turns: int(a.pairTurns)}}
}

// usageRollupQuery takes an optional room clause, so a room read searches the
// (room_id, day) index.
const usageRollupQuery = `SELECT day, role, model, SUM(turns), SUM(wall_ms),
  SUM(input), SUM(input_complete), SUM(input_partial), SUM(input_unknown),
  SUM(cached), SUM(cached_complete), SUM(cached_partial), SUM(cached_unknown),
  SUM(output), SUM(output_complete), SUM(output_partial), SUM(output_unknown),
  SUM(cost_usd), SUM(cost_complete), SUM(cost_partial), SUM(cost_unknown),
  SUM(pair_input), SUM(pair_cached), SUM(pair_turns)
FROM usage_rollup WHERE %s day BETWEEN ? AND ? GROUP BY day, role, model`

func usageRollupSQL(roomID, fromDay, toDay string) (string, []any) {
	if roomID == "" {
		return fmt.Sprintf(usageRollupQuery, ""), []any{fromDay, toDay}
	}
	return fmt.Sprintf(usageRollupQuery, "room_id = ? AND"), []any{roomID, fromDay, toDay}
}

// ReadUsageRollup sums rollup rows for days in [fromDay, toDay] (YYYY-MM-DD,
// UTC) with one indexed query. An empty roomID reads every room.
func (s *Store) ReadUsageRollup(roomID, fromDay, toDay string) (UsageRollupSnapshot, error) {
	out := UsageRollupSnapshot{RoomID: roomID, From: fromDay, To: toDay, ByDayModel: []UsageRollupRow{}, ByRoleModel: []UsageRollupRow{}}
	db, err := s.usageDB()
	if err != nil {
		return out, err
	}
	query, args := usageRollupSQL(roomID, fromDay, toDay)
	rows, err := db.Query(query, args...)
	if err != nil {
		return out, err
	}
	type key struct{ a, b string }
	byDay, byRole := map[key]*usageSums{}, map[key]*usageSums{}
	var total usageSums
	for rows.Next() {
		var day, role, model string
		var g usageSums
		if err := rows.Scan(&day, &role, &model, &g.turns, &g.wallMs,
			&g.input[0], &g.input[1], &g.input[2], &g.input[3],
			&g.cached[0], &g.cached[1], &g.cached[2], &g.cached[3],
			&g.output[0], &g.output[1], &g.output[2], &g.output[3],
			&g.cost, &g.costComplete, &g.costPartial, &g.costUnk,
			&g.pairInput, &g.pairCached, &g.pairTurns); err != nil {
			rows.Close()
			return out, err
		}
		total.add(g)
		for _, group := range []struct {
			into map[key]*usageSums
			k    key
		}{{byDay, key{day, model}}, {byRole, key{role, model}}} {
			if group.into[group.k] == nil {
				group.into[group.k] = &usageSums{}
			}
			group.into[group.k].add(g)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	out.Totals = total.row()
	for k, g := range byDay {
		r := g.row()
		r.Day, r.Model = k.a, k.b
		out.ByDayModel = append(out.ByDayModel, r)
	}
	for k, g := range byRole {
		r := g.row()
		r.Role, r.Model = k.a, k.b
		out.ByRoleModel = append(out.ByRoleModel, r)
	}
	sort.Slice(out.ByDayModel, func(i, j int) bool {
		a, b := out.ByDayModel[i], out.ByDayModel[j]
		return a.Day < b.Day || a.Day == b.Day && a.Model < b.Model
	})
	sort.Slice(out.ByRoleModel, func(i, j int) bool {
		a, b := out.ByRoleModel[i], out.ByRoleModel[j]
		return a.Role < b.Role || a.Role == b.Role && a.Model < b.Model
	})
	return out, nil
}

// RunUsageRepair backfills at start, then reconciles hourly, until ctx ends.
func (s *Store) RunUsageRepair(ctx context.Context, report func(error)) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := s.ReconcileUsage(ctx); err != nil && ctx.Err() == nil {
			report(fmt.Errorf("usage rollup: %w", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

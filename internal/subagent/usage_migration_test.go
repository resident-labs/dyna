package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/workeroutput"
)

func TestMigratePersistsCurrentTurnUsageShape(t *testing.T) {
	s := rollupStore(t)
	const id = "ses_20261019-120000-aaaaaaaa"
	a, b, boundary := claudeTurn("", 3), claudeTurn("", 5), claudeTurn("", 9)
	a.HarnessSession, b.HarnessSession, boundary.HarnessSession = "native", "native", "other"
	current := claudeTurn(workeroutput.CostBasisTurn, 0)
	seedRollupSession(t, s, id, "r1", "", a, b, boundary, Turn{Status: TurnOK}, current)
	// A third turn must use the original second total, not its converted step.
	chain := []Turn{claudeTurn("", 3), claudeTurn("", 5), claudeTurn("", 9)}
	for i := range chain {
		chain[i].HarnessSession = "chain"
	}
	const other = "ses_20261019-120000-bbbbbbbb"
	seedRollupSession(t, s, other, "", "", chain...)
	db, err := s.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := storedTurn(context.Background(), db, id, 2)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	body["future"] = json.RawMessage(`{"kept":true}`)
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(body["usage"], &usage); err != nil {
		t.Fatal(err)
	}
	usage["future"] = json.RawMessage(`17`)
	body["usage"], _ = json.Marshal(usage)
	var norm map[string]json.RawMessage
	if err := json.Unmarshal(body["usageNormalization"], &norm); err != nil {
		t.Fatal(err)
	}
	norm["future"] = json.RawMessage(`"kept"`)
	body["usageNormalization"], _ = json.Marshal(norm)
	raw, _ = json.Marshal(body)
	if _, err := db.Exec("UPDATE session_turns SET turn=? WHERE session=? AND n=2", raw, id); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(s.root(), id, "turns", "2.json")
	before := append([]byte(nil), raw...)
	if err := os.WriteFile(source, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(filepath.Dir(s.root())); err != nil {
		t.Fatal(err)
	}
	third, err := s.ReadTurn(other, 3)
	if err != nil || third.Usage.CostUSD != 4 {
		t.Errorf("original predecessor lost: %+v, %v", third.Usage, err)
	}
	for n, want := range map[int]float64{1: 3, 2: 2, 3: 9, 5: 0} {
		turn, err := s.ReadTurn(id, n)
		if err != nil || turn.Usage.CostUSD != want {
			t.Errorf("turn %d cost = %v, %v; want %v", n, turn.Usage.CostUSD, err, want)
		}
	}
	for _, n := range []int{3, 4} {
		turn, err := s.ReadTurn(id, n)
		if err != nil || turn.UsageNormalization.Version != 1 || turn.UsageNormalization.Cost != "unknown" {
			t.Errorf("turn %d coverage = %+v, %v", n, turn.UsageNormalization, err)
		}
	}
	raw, revision, err := storedTurn(context.Background(), db, id, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"future":{"kept":true}`)) || !bytes.Contains(raw, []byte(`"future":17`)) || !bytes.Contains(raw, []byte(`"future":"kept"`)) {
		t.Error("unknown members lost")
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(before, after) {
		t.Error("source changed")
	}
	if err := s.ReconcileUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s).Totals.Cost; got.USD == nil || *got.USD != 5 {
		t.Errorf("chain cost = %+v", got)
	}
	data := filepath.Dir(s.root())
	if err := database.Close(data); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(data); err != nil {
		t.Fatal(err)
	}
	db, err = s.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	replay, rev, err := storedTurn(context.Background(), db, id, 2)
	if err != nil || rev != revision || !bytes.Equal(raw, replay) {
		t.Error("migration replay changed row")
	}
}

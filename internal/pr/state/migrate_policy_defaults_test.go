package state

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func TestMigrationPersistsBudgetDefaultsAndDisabledReview(t *testing.T) {
	root := newRoot(t)
	db, err := database.Open(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	raw := `{"policies":{"partial":{"review":"on","ciNoJUnitWarning":false,"opaque":9007199254740993}},"reviewers":{"disabled":{"disabled":true},"partial":{"disabled":true}},"future":9007199254740993}`
	if _, err := db.Exec("INSERT INTO pr_state VALUES(1,1,?)", raw); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(filepath.Dir(root)); err != nil {
		t.Fatal(err)
	}
	var body string
	var revision int
	if err := db.QueryRow("SELECT body,revision FROM pr_state").Scan(&body, &revision); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Policies map[string]map[string]json.RawMessage `json:"policies"`
		Future   json.RawMessage                       `json:"future"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	for _, room := range []string{"partial", "disabled"} {
		p := doc.Policies[room]
		for key, want := range map[string]string{"ciTimeBudget": `"warn"`, "ciWarnAfter": `"2m"`, "ciFailAfter": `"3m"`, "ciTestMax": `"10s"`} {
			if string(p[key]) != want {
				t.Fatalf("%s.%s = %s; want %s", room, key, p[key], want)
			}
		}
	}
	if string(doc.Policies["partial"]["ciNoJUnitWarning"]) != "false" || string(doc.Policies["partial"]["review"]) != `"on"` || string(doc.Policies["partial"]["opaque"]) != "9007199254740993" || string(doc.Future) != "9007199254740993" {
		t.Fatal(body)
	}
	if string(doc.Policies["disabled"]["review"]) != `"off"` || string(doc.Policies["disabled"]["ciNoJUnitWarning"]) != "true" {
		t.Fatal(body)
	}
	if err := Migrate(filepath.Dir(root)); err != nil {
		t.Fatal(err)
	}
	var replay string
	var next int
	if err := db.QueryRow("SELECT body,revision FROM pr_state").Scan(&replay, &next); err != nil {
		t.Fatal(err)
	}
	if replay != body || next != revision {
		t.Fatal("migration replay changed state")
	}
}

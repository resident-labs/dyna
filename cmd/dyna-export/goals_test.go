package main

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestReadGoalsIncludesOnlyActiveGoalsAndTheirItems(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE goals (room TEXT, id TEXT, state TEXT, body TEXT);
CREATE TABLE goal_items (room TEXT, goal TEXT, id TEXT, text TEXT, state TEXT, assignee TEXT);
INSERT INTO goals VALUES
 ('r','g1','active','{"id":"g1","text":"active","setter":"owner"}'),
 ('r','g2','done','{"id":"g2","text":"done","setter":"owner"}');
INSERT INTO goal_items VALUES
 ('r','g1','i1','item','done','worker'), ('r','g2','i1','old','open','');`)
	if err != nil {
		t.Fatal(err)
	}
	author := func(id string) (contract.ImportedAuthor, error) {
		return contract.ImportedAuthor{Label: contract.V1Address("label:" + id)}, nil
	}
	got, err := readGoals(db, "r", author)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Items) != 1 || got[0].Items[0].Assignee != "label:worker" || got[0].Setter.Label != "label:owner" {
		t.Fatalf("unexpected goals: %+v", got)
	}
}

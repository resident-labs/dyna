package main

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestReadQuestionsPreservesTerminalArguments(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE questions(key TEXT, room TEXT, state TEXT, body TEXT);
INSERT INTO questions VALUES ('q1','r','open','{"id":"q1","asker":"worker","kind":"terminal","terminal":{"argv":["echo","a b"],"cwd":"/tmp","reason":"test"}}'),
 ('q2','r','answered','{"id":"q2"}');`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readQuestions(db, "r", func(id string) (contract.ImportedAuthor, error) {
		return contract.ImportedAuthor{Label: contract.V1Address(id)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != contract.QuestionTerminal || len(got[0].Argv) != 2 || got[0].Argv[1] != "a b" || got[0].Cwd != "/tmp" || got[0].Reason != "test" {
		t.Fatalf("unexpected questions: %+v", got)
	}
}

package main

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestTrackedPRPreservesHold(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE pr_rows(room TEXT,repo TEXT,number INTEGER,closed INTEGER,body TEXT);
INSERT INTO pr_rows VALUES ('r','owner/repo',7,0,'{"record":{"host":"github.com","authorSession":"alice","hold":{"reason":"review","by":"bob","at":"2026-01-01T00:00:00Z"}}}');`)
	if err != nil {
		t.Fatal(err)
	}
	author := func(s string) (contract.ImportedAuthor, error) {
		return contract.ImportedAuthor{Kind: contract.PrincipalSession, Label: contract.V1Address(s)}, nil
	}
	got, _, err := readPRs(db, "r", author)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Hold == nil || got[0].Hold.Reason != "review" || got[0].Hold.By.Label != "bob" || got[0].Hold.At.IsZero() {
		t.Fatalf("hold = %+v", got)
	}
}

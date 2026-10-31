package main

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestResolveAuthor(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE session_records(id TEXT, room TEXT, name TEXT); INSERT INTO session_records VALUES ('ses_1','room','root')`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		input string
		want  contract.ImportedAuthor
	}{
		{"owner", contract.ImportedAuthor{Kind: contract.PrincipalOwner, Label: "owner"}},
		{"engine", contract.ImportedAuthor{Kind: contract.PrincipalEngine, Label: "engine"}},
		{"ses_1", contract.ImportedAuthor{Kind: contract.PrincipalSession, Label: "room:root"}},
	} {
		got, err := resolveAuthor(db, tc.input)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.input, got, err, tc.want)
		}
	}
}

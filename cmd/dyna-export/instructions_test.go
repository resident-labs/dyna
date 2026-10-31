package main

import (
	"database/sql"
	"testing"
)

func TestInstructionsRoundTripAndDetectChangedText(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE goal_rooms(room TEXT, body TEXT); INSERT INTO goal_rooms VALUES('r',?);`, `{"rules":{"r2":{"id":"r2","text":"Line\n\n\u0060\u0060\u0060\n  x\n\u0060\u0060\u0060\n"},"r1":{"id":"r1","text":"First"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	text, err := renderInstructions(db, "r")
	if err != nil {
		t.Fatal(err)
	}
	want := "# Instructions\n\n- First\n- Line\n  \n  ```\n    x\n  ```\n  \n"
	if text != want {
		t.Fatalf("got %q, want %q", text, want)
	}
	if err := verifyInstructions(db, "r", text); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstructions(db, "r", text+"- injected\n"); err == nil {
		t.Fatal("accepted extra rule")
	}
}

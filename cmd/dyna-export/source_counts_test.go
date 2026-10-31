package main

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCountRoomSQL(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE goal_rooms (room TEXT, body TEXT);
CREATE TABLE goals (room TEXT, id TEXT, state TEXT);
CREATE TABLE goal_items (room TEXT, goal TEXT);
CREATE TABLE room_issues (room_id TEXT, state TEXT);
CREATE TABLE questions (room TEXT, state TEXT);
CREATE TABLE pr_rows (room TEXT, closed INTEGER, body TEXT);
CREATE TABLE session_records (room_id TEXT, closed INTEGER, history_only INTEGER, service INTEGER, meta TEXT);
INSERT INTO goals VALUES ('r','g1','active'),('r','g2','done'),('other','g1','active');
INSERT INTO goal_items VALUES ('r','g1'),('r','g1'),('r','g2'),('other','g1');
INSERT INTO room_issues VALUES ('r','open'),('r','goal'),('r','closed'),('other','open');
INSERT INTO questions VALUES ('r','open'),('r','answered'),('other','open');`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := countRoomSQL(db, "r")
	if err != nil {
		t.Fatal(err)
	}
	want := (roomSQLCounts{Goals: 1, GoalItems: 2, Issues: 2, Questions: 1})
	if got != want {
		t.Fatalf("counts = %+v, want %+v", got, want)
	}
}

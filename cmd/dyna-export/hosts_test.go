package main

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestReadHostsKeepsEnabledRemoteHosts(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE machines(name TEXT,ssh TEXT,enabled INTEGER,local INTEGER,labels TEXT,rooms TEXT);
INSERT INTO machines VALUES ('local','','1','1','[]','[]'),('off','','0','0','[]','[]'),
 ('remote','user@host','1','0','["gpu"]','["r1"]');`)
	if err != nil {
		t.Fatal(err)
	}
	hosts, skipped, err := readHosts(db, map[string]contract.RoomName{"r1": "room"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].Name != "remote" || len(hosts[0].Rooms) != 1 || hosts[0].Rooms[0] != "room" || len(skipped) != 2 {
		t.Fatalf("unexpected hosts: %+v, skipped: %+v", hosts, skipped)
	}
}

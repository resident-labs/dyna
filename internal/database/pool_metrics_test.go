package database

import (
	"context"
	"testing"
)

func TestMeasurementsReportsInUseConnections(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer Close(dir)
	before := Measurements().Pool
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	during := Measurements().Pool
	if during.InUse != before.InUse+1 || during.Open < during.InUse || during.Idle != during.Open-during.InUse {
		t.Fatalf("pool before %+v; pool with borrowed connection %+v", before, during)
	}
}

package database

import (
	"context"
	"database/sql"
	"testing"
)

func TestEveryConnectionEnforcesForeignKeys(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(dir) })
	if _, err := db.Exec(`CREATE TABLE parent(id INTEGER PRIMARY KEY);
CREATE TABLE child(parent_id INTEGER NOT NULL REFERENCES parent(id) ON DELETE CASCADE);`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Hold the connections together so the pool must open each one.
	var connections []*sql.Conn
	for range 3 {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		connections = append(connections, conn)
	}
	for i, conn := range connections {
		if _, err := conn.ExecContext(ctx, "INSERT INTO child VALUES(99)"); err == nil {
			t.Fatalf("connection %d accepted a missing parent", i)
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO parent VALUES(?);", i); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO child VALUES(?)", i); err != nil {
			t.Fatal(err)
		}
		// Delete from another connection: enforcement belongs to the writer,
		// not the connection that created the child.
		other := connections[(i+1)%len(connections)]
		if _, err := other.ExecContext(ctx, "DELETE FROM parent WHERE id=?", i); err != nil {
			t.Fatal(err)
		}
		var children int
		if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM child").Scan(&children); err != nil {
			t.Fatal(err)
		}
		if children != 0 {
			t.Fatalf("connection %d left %d children after parent deletion", (i+1)%len(connections), children)
		}
	}
}

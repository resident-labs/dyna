package room

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestRoomColorsImportAndCreate(t *testing.T) {
	s := recordsStore(t)
	db, err := s.recordsDB()
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(db, "fixture.rooms", 1, func(conn *sql.Conn, _ int) error {
		for i, name := range []string{"first", "second"} {
			rec := Record{ID: name, Name: name, CreatedAt: time.Unix(int64(i), 0)}
			if err := putRecord(context.Background(), conn, rec); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.migrateRecords(); err != nil {
		t.Fatal(err)
	}
	first, err := s.readRecord("first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.readRecord("second")
	if err != nil {
		t.Fatal(err)
	}
	if first.Color != ColorCrimson || second.Color != ColorBlue {
		t.Fatalf("import: %q %q", first.Color, second.Color)
	}
	third, err := s.Create(CreateInput{Name: "third"})
	if err != nil {
		t.Fatal(err)
	}
	if third.Color != ColorViolet {
		t.Fatalf("create: %q", third.Color)
	}
}

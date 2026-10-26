package state

import (
	"context"
	"testing"
)

// A long read transaction stops WAL checkpoints from finishing, so the WAL
// and the later COMMIT that checkpoints it both grow. A snapshot refresh must
// end its read transaction before it decodes the stored JSON.
func TestSnapshotDecodesAfterTheReadTransactionEnds(t *testing.T) {
	root := newRoot(t)
	if err := Write(root, bigState(2)); err != nil {
		t.Fatal(err)
	}
	db := mustOpen(t, root)
	forgetSnapshot()

	orig := unmarshalRecord
	var decoded int
	var busy []int
	unmarshalRecord = func(data []byte, record *diskRecord) error {
		// A TRUNCATE checkpoint reports busy while any read transaction is open.
		var b, logFrames, moved int
		err := db.QueryRowContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&b, &logFrames, &moved)
		if err != nil {
			t.Error(err)
		}
		busy = append(busy, b)
		decoded++
		return orig(data, record)
	}
	defer func() { unmarshalRecord = orig }()

	if _, err := Read(root); err != nil {
		t.Fatal(err)
	}
	if decoded == 0 {
		t.Fatal("the refresh decoded no records")
	}
	for i, b := range busy {
		if b != 0 {
			t.Fatalf("decode %d ran while a read transaction was open (checkpoint busy = %d)", i, b)
		}
	}
}

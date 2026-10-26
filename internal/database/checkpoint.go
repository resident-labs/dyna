package database

import (
	"context"
	"database/sql"
	"time"
)

// A COMMIT that crosses the automatic checkpoint threshold copies the whole
// WAL into dyna.db and syncs it while it still holds the writer lock. That was
// 72% of all held write time (a COMMIT of 0.5-3.7 s after a body of 1 ms). The
// engine checkpoints on a timer instead, with PASSIVE, which takes no writer
// lock. The automatic threshold stays as a backstop for processes that run
// without an engine.
const (
	// backstopPages is the automatic checkpoint threshold: 64 MB of 4 KB pages.
	backstopPages = 16384
	// walSizeLimit shrinks the WAL file to this size after a checkpoint resets it.
	walSizeLimit = 64 << 20
	// CheckpointEvery is how often the engine checkpoints dyna.db.
	CheckpointEvery = 10 * time.Second
)

// Checkpointed reports one WAL checkpoint.
type Checkpointed struct {
	Busy   bool // a reader or checkpoint kept it from finishing
	Frames int  // frames in the WAL
	Moved  int  // frames copied into the database file
}

// Checkpoint copies committed WAL frames into dyna.db without taking the
// writer lock. It does not wait for readers or writers.
func Checkpoint(ctx context.Context, db *sql.DB) (Checkpointed, error) {
	var busy int
	var c Checkpointed
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &c.Frames, &c.Moved); err != nil {
		return Checkpointed{}, err
	}
	c.Busy = busy != 0
	return c, nil
}

// KeepCheckpointed checkpoints dataDir's database on every tick until ctx ends.
func KeepCheckpointed(ctx context.Context, dataDir string, ticks <-chan time.Time) {
	db, err := Open(dataDir)
	if err != nil {
		logf("database: checkpoint loop: %v", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if _, err := Checkpoint(ctx, db); err != nil && ctx.Err() == nil {
				logf("database: checkpoint: %v", err)
			}
		}
	}
}

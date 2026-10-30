package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
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
	// slowCheckpoint is the scheduled checkpoint time that is logged.
	slowCheckpoint = 100 * time.Millisecond
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
			checkpointTick(ctx, db, dataDir)
		}
	}
}

// checkpointTick runs one scheduled checkpoint, records it and logs it when slow.
func checkpointTick(ctx context.Context, db *sql.DB, dataDir string) {
	start := now()
	c, err := Checkpoint(ctx, db)
	d := now().Sub(start)
	recordCheckpoint(d, c, err)
	if err != nil {
		if ctx.Err() == nil {
			logf("database: checkpoint: %v", err)
		}
		return
	}
	logSlowCheckpoint(d, c, dataDir)
}

// logSlowCheckpoint logs a checkpoint that took slowCheckpoint or longer, or
// left the WAL busy. It names the WAL size, so a long WAL can be matched to a slow run.
func logSlowCheckpoint(d time.Duration, c Checkpointed, dataDir string) {
	if d < slowCheckpoint && !c.Busy {
		return
	}
	logf("database: slow checkpoint took %s (busy %t, frames %d, moved %d, WAL %d bytes); %s",
		d.Round(time.Millisecond), c.Busy, c.Frames, c.Moved, walBytes(dataDir), ioBlame())
}

// walBytes is the size of dyna.db-wal, or 0 when the file does not exist.
func walBytes(dataDir string) int64 {
	info, err := os.Stat(filepath.Join(dataDir, "dyna.db-wal"))
	if err != nil {
		return 0
	}
	return info.Size()
}

package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// A writer transaction held longer than slowWrite, or a read transaction
// held longer than slowRead, is logged with its process and caller, so a
// stall names its holder (R3.2).
const (
	slowWrite = 100 * time.Millisecond
	slowRead  = time.Second
)

// now and logf are replaced by tests.
var (
	now  = time.Now
	logf = log.Printf
)

// Read runs fn on one read-only transaction and logs it when its BEGIN wait,
// body and COMMIT together take longer than slowRead.
func Read(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	start := now()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	began := now()
	var committing time.Time
	defer tx.Rollback()
	defer func() {
		finished := now()
		if committing.IsZero() {
			committing = finished
		}
		logSlowRead(began.Sub(start), committing.Sub(began), finished.Sub(committing))
	}()
	if err := fn(tx); err != nil {
		return err
	}
	committing = now()
	return tx.Commit()
}

// logSlowRead logs a slow read with its BEGIN wait, body and COMMIT apart.
func logSlowRead(wait, body, commit time.Duration) {
	held := wait + body + commit
	if held <= slowRead {
		return
	}
	logf("database: slow read held %s (body %s, commit %s) after waiting %s for BEGIN (%s)",
		held.Round(time.Millisecond), body.Round(time.Millisecond), commit.Round(time.Millisecond), wait.Round(time.Millisecond), holder())
}

// logSlowWrite logs a writer transaction whose body and COMMIT together held
// the lock longer than slowWrite. The wait for BEGIN is shown apart: a wait
// in whole multiples of busy_timeout points to another process, and a long
// COMMIT to the automatic checkpoint that runs inside it.
func logSlowWrite(wait, body, commit time.Duration) {
	if body+commit <= slowWrite {
		return
	}
	logf("database: slow write held %s (body %s, commit %s) after waiting %s for BEGIN (%s)",
		(body + commit).Round(time.Millisecond), body.Round(time.Millisecond), commit.Round(time.Millisecond), wait.Round(time.Millisecond), holder())
}

// holder names this process and the first caller outside this package and
// internal/state. It runs only on the slow path.
func holder() string {
	return fmt.Sprintf("%s[%d], caller %s", process(), os.Getpid(), caller())
}

// caller is the first function on the stack outside this package and
// internal/state: the store that asked for the transaction.
func caller() string {
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs)])
	for {
		frame, more := frames.Next()
		if !strings.HasPrefix(frame.Function, "dyna-agent/internal/database.") && !strings.HasPrefix(frame.Function, "dyna-agent/internal/state.") {
			return frame.Function
		}
		if !more {
			return "unknown"
		}
	}
}

// writerCaller names the store that asked for a writer transaction. It runs
// only when caller detail is on, so the stack walk costs nothing otherwise.
func writerCaller() string {
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	var stack []string
	for {
		frame, more := frames.Next()
		stack = append(stack, frame.Function)
		if !more {
			return writerLabel(stack)
		}
	}
}

// writerLabel picks the label from a stack, innermost first. The first caller
// outside this package and internal/state is the store. A goals store read
// also names the first caller outside internal/goals, which asked for the
// barrier: "goals.(*Store).list via worktrees.(*Store).SessionClosed".
func writerLabel(stack []string) string {
	first := ""
	for _, fn := range stack {
		if strings.HasPrefix(fn, "dyna-agent/internal/database.") || strings.HasPrefix(fn, "dyna-agent/internal/state.") {
			continue
		}
		inGoals := strings.HasPrefix(fn, "dyna-agent/internal/goals.")
		if first == "" {
			first = fn
			if !inGoals {
				return fn
			}
			continue
		}
		if !inGoals {
			return first + " via " + fn
		}
	}
	if first == "" {
		return "unknown"
	}
	return first
}

// process is the program and its subcommand, such as "dyna _turn".
func process() string {
	p := filepath.Base(os.Args[0])
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		p += " " + os.Args[1]
	}
	return p
}

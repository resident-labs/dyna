package runstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/workeroutput"
	"dyna-agent/internal/workeroutput/projection"
)

// TODO: legacy/shim remove later — converts retained workflow output once.
// migrateOutput converts the retained prefix of each workflow agent once.
// Preparing one line at a time keeps source bodies outside the SQL writer.
// Each artifact commits independently; the shared checkpoint also records
// empty or unchanged prefixes, so a retry never recaptures a grown artifact.
func migrateOutput(db *sql.DB, dataDir string) error {
	const key = "runs.output_projection"
	var version int
	err := db.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if version < 0 {
		return fmt.Errorf("%s has invalid version %d", key, version)
	}
	complete := func(*sql.Conn, int) error { return nil }
	if version != 0 || !database.OwnsSchema() {
		return database.Migrate(db, key, 1, complete)
	}
	archive := filepath.Join(dataDir, "archive", "runs")
	if configured := strings.TrimSpace(os.Getenv(ArchiveDirEnv)); configured != "" {
		archive, err = filepath.Abs(configured)
		if err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, root := range []string{filepath.Join(dataDir, "runs"), archive} {
		runs, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, run := range runs {
			id := run.Name()
			if !run.IsDir() || validateRunID(id) != nil || seen[id] {
				continue
			}
			seen[id] = true
			agentsDir := filepath.Join(root, id, "agents")
			info, err := os.Lstat(agentsDir)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return fmt.Errorf("run output agents path is not a directory: %s", agentsDir)
			}
			agents, err := os.ReadDir(agentsDir)
			if err != nil {
				return err
			}
			for _, agent := range agents {
				number, err := strconv.Atoi(agent.Name())
				if err != nil || validateAgentID(number) != nil || strconv.Itoa(number) != agent.Name() {
					continue
				}
				if !agent.IsDir() {
					return fmt.Errorf("run output agent path is not a directory: %s", filepath.Join(agentsDir, agent.Name()))
				}
				artifact, err := AgentNormalizedOutputKey(id, number)
				if err != nil {
					return err
				}
				if err := migrateOutputArtifact(db, artifact, filepath.Join(agentsDir, agent.Name(), "output.jsonl")); err != nil {
					return err
				}
			}
		}
	}
	return database.Migrate(db, key, 1, complete)
}

func migrateOutputArtifact(db *sql.DB, key, path string) error {
	ctx := context.Background()
	converted, err := projection.Converted(ctx, db, key)
	if err != nil || converted {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("run output is not a regular file: %s", path)
	}
	captured := info.Size()
	pack, err := projection.NewPack(key, path)
	if err != nil {
		return err
	}
	defer pack.Abort()
	var end int64
	// A zero visitor limit means unbounded input, so an empty capture must skip it.
	if captured > 0 {
		end, err = VisitNormalizedOutputLines(path, 0, captured, func(line projection.Line) error {
			if len(line.Raw) == 0 {
				return nil
			}
			// Retained workflow output needs no field repair: every current
			// dialect writes complete tool events.
			return pack.Add([]projection.Line{line}, func([]workeroutput.Event) []int { return nil })
		})
		if err != nil {
			return err
		}
	}
	prepared, err := pack.Finish(captured, end)
	if err != nil {
		return err
	}
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		converted, err := projection.Converted(ctx, conn, key)
		if err != nil || converted {
			return err
		}
		return prepared.Commit(ctx, conn)
	})
	return errors.Join(err, prepared.Close())
}

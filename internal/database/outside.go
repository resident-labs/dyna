package database

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Item 41, report only: until the CLI and the detached runners open dyna.db
// read-only, a process that does not own the schema notes each call site
// that writes, in <data>/outside-writes.jsonl. The file keeps one line per
// site across all processes, and stops at outsideCap. Naming the site walks
// the stack on every write outside the engine, a few microseconds that only
// the CLI and runners pay; the engine skips it.

// OutsideWritesFile is the report's name in the data folder.
const OutsideWritesFile = "outside-writes.jsonl"

const outsideCap = 1 << 20

// outside is this process's view of the report: the sites already in it.
var outside struct {
	sync.Mutex
	seen map[string]bool
	full bool
}

// noteOutsideWrite adds the caller of a write to the report, once per site.
// It never fails the write: a report it cannot read or write is skipped.
func noteOutsideWrite(db *sql.DB) {
	if migrates.Load() {
		return
	}
	dir := folderOf(db)
	if dir == "" {
		return
	}
	site := caller()
	outside.Lock()
	defer outside.Unlock()
	if outside.seen == nil {
		outside.seen = map[string]bool{}
	}
	if outside.full || outside.seen[site] {
		return
	}
	path := filepath.Join(dir, OutsideWritesFile)
	// Another process may have noted the site already.
	if b, err := os.ReadFile(path); err == nil {
		if len(b) >= outsideCap {
			outside.full = true
			logf("database: %s reached %d bytes; no more sites are noted", path, outsideCap)
			return
		}
		for _, l := range bytes.Split(b, []byte{'\n'}) {
			var e struct {
				Site string `json:"site"`
			}
			if json.Unmarshal(l, &e) == nil {
				outside.seen[e.Site] = true
			}
		}
	}
	if outside.seen[site] {
		return
	}
	outside.seen[site] = true
	line, err := json.Marshal(struct {
		TS      time.Time `json:"ts"`
		Site    string    `json:"site"`
		Process string    `json:"process"`
	}{now(), site, process()})
	if err != nil {
		return
	}
	appendLine(path, line)
}

// appendLine adds one line in one write, so lines from several processes do
// not interleave.
func appendLine(path string, line []byte) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

// folderOf is the data folder of an open handle, or "" for one not opened
// through Open.
func folderOf(db *sql.DB) string {
	handles.Lock()
	defer handles.Unlock()
	for folder, h := range handles.byFolder {
		if h == db {
			return folder
		}
	}
	return ""
}

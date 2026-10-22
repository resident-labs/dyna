// Package projection prepares inactive, immutable conversions of retained output.
// Durable mappings and conversion checkpoints belong in SQLite, not pack files.
package projection

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"

	"dyna-agent/internal/workeroutput"
)

// Line is one complete source line, including its newline and absolute range.
type Line struct {
	Start, End int64
	Raw        []byte
}

// Row maps one source range to canonical bytes and verifies both representations.
type Row struct {
	Start, End                    int64
	SourceDigest, CanonicalDigest [sha256.Size]byte
	CanonicalStart, CanonicalEnd  int64
}

const rowSize = 96
const rowBuffer = 256

// Pack prepares one artifact. Add units must have nondecreasing starts, with
// contiguous complete lines inside each unit. Overlap checks read fixed-size
// rows from the temporary spool, so only the current unit stays in memory.
type Pack struct {
	key, dir              string
	tmp, meta             *os.File
	mappings              *bufio.Writer
	sum                   hash.Hash
	size, rows, from, end int64
	added, closed         bool
}

func NewPack(key, path string) (*Pack, error) {
	if key == "" || path == "" {
		return nil, errors.New("pack requires an artifact key and path")
	}
	return &Pack{key: key, dir: filepath.Dir(path), sum: sha256.New()}, nil
}

// Add converts a unit. The callback changes events in place and returns their
// indices. Malformed source lines stay unmapped but still participate in overlap
// validation. Any failure aborts preparation; no partial pack can be finished.
func (p *Pack) Add(lines []Line, convert func([]workeroutput.Event) []int) (err error) {
	if p.closed {
		return errors.New("pack is closed")
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.Abort())
		}
	}()
	if len(lines) == 0 {
		return nil
	}
	from, end := lines[0].Start, lines[len(lines)-1].End
	if p.added && from < p.from {
		return errors.New("pack units must have nondecreasing starts")
	}
	for i, line := range lines {
		if line.Start < 0 || line.End <= line.Start || line.End-line.Start != int64(len(line.Raw)) || line.Raw[len(line.Raw)-1] != '\n' || (i > 0 && lines[i-1].End != line.Start) {
			return errors.New("invalid or noncontiguous source line range")
		}
	}
	if p.meta == nil {
		if err = p.open(); err != nil {
			return err
		}
	}
	if err = p.mappings.Flush(); err != nil {
		return err
	}
	previous := p.rows
	events := make([]workeroutput.Event, 0, len(lines))
	indices := make([]int, len(lines))
	for i, line := range lines {
		indices[i] = -1
		var event workeroutput.Event
		if json.Unmarshal(line.Raw, &event) == nil {
			indices[i] = len(events)
			events = append(events, event)
		}
	}
	changed := make([]bool, len(events))
	for _, i := range convert(events) {
		if i < 0 || i >= len(events) {
			return errors.New("conversion returned an invalid event index")
		}
		changed[i] = true
	}
	for i, line := range lines {
		canonical := line.Raw
		change := false
		if index := indices[i]; index >= 0 {
			canonical, err = json.Marshal(events[index])
			if err != nil {
				return err
			}
			canonical = append(canonical, '\n')
			change = changed[index]
		}
		row := Row{Start: line.Start, End: line.End, SourceDigest: sha256.Sum256(line.Raw), CanonicalDigest: sha256.Sum256(canonical), CanonicalStart: -1, CanonicalEnd: -1}
		old, found, err := p.findRow(previous, line.Start, line.End)
		if err != nil {
			return err
		}
		if found {
			if old.SourceDigest != row.SourceDigest || old.CanonicalDigest != row.CanonicalDigest || (old.CanonicalStart >= 0) != change {
				return fmt.Errorf("source range at %d converts differently in overlapping units", line.Start)
			}
			continue
		}
		if change {
			if p.tmp == nil {
				p.tmp, err = os.CreateTemp(p.dir, ".output.canonical-*.tmp")
				if err != nil {
					return err
				}
			}
			if _, err = io.MultiWriter(p.tmp, p.sum).Write(canonical); err != nil {
				return err
			}
			row.CanonicalStart, row.CanonicalEnd = p.size, p.size+int64(len(canonical))
			p.size = row.CanonicalEnd
		}
		if err = binary.Write(p.mappings, binary.LittleEndian, row); err != nil {
			return err
		}
		p.rows++
	}
	p.from, p.end, p.added = from, max(p.end, end), true
	return nil
}
func (p *Pack) open() (err error) {
	p.meta, err = os.CreateTemp(p.dir, ".output.rows-*.tmp")
	if err != nil {
		return err
	}
	if err = os.Remove(p.meta.Name()); err != nil {
		return err
	}
	p.mappings = bufio.NewWriterSize(p.meta, rowBuffer*rowSize)
	return nil
}

// findRow searches only the previous units, already flushed in source order.
// Unchanged lines also have temporary rows, so their overlaps are checked.
func (p *Pack) findRow(count, start, end int64) (Row, bool, error) {
	lo, hi := int64(0), count
	for lo < hi {
		mid := lo + (hi-lo)/2
		var row Row
		if err := binary.Read(io.NewSectionReader(p.meta, mid*rowSize, rowSize), binary.LittleEndian, &row); err != nil {
			return row, false, err
		}
		if row.End <= start {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == count {
		return Row{}, false, nil
	}
	var row Row
	if err := binary.Read(io.NewSectionReader(p.meta, lo*rowSize, rowSize), binary.LittleEndian, &row); err != nil {
		return row, false, err
	}
	if row.Start >= end {
		return Row{}, false, nil
	}
	if row.Start != start || row.End != end {
		return row, false, errors.New("source ranges overlap with different boundaries")
	}
	return row, true, nil
}

// Prepared owns the temporary mapping spool. Close it after the caller commits
// mappings, or after any failure. Its blob is immutable and remains inactive
// until that later SQLite transaction records its mappings and checkpoint.
type Prepared struct {
	Key, Pack               string
	CapturedSize, PrefixEnd int64
	meta                    *os.File
	rows                    int64
}

func (p *Prepared) Close() error {
	if p.meta == nil {
		return nil
	}
	err := p.meta.Close()
	p.meta = nil
	return err
}

// EachRow visits converted mapping rows with constant memory.
func (p *Prepared) EachRow(fn func(Row) error) error {
	if p.rows == 0 {
		return nil
	}
	if p.meta == nil {
		return errors.New("prepared mappings are closed")
	}
	info, err := p.meta.Stat()
	if err != nil {
		return err
	}
	if info.Size() != p.rows*rowSize {
		return errors.New("prepared mapping size changed")
	}
	reader := io.NewSectionReader(p.meta, 0, info.Size())
	for i := int64(0); i < p.rows; i++ {
		var row Row
		if err := binary.Read(reader, binary.LittleEndian, &row); err != nil {
			return err
		}
		if row.CanonicalStart < 0 {
			continue
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return nil
}

// Finish syncs and publishes the content-addressed blob. Empty conversions
// publish no blob but still return the captured prefix for a SQLite checkpoint.
func (p *Pack) Finish(capturedSize, prefixEnd int64) (prepared Prepared, err error) {
	if p.closed {
		return prepared, errors.New("pack is closed")
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.Abort())
		}
	}()
	if prefixEnd < 0 || prefixEnd > capturedSize || p.end > prefixEnd {
		return prepared, errors.New("pack source range exceeds captured prefix")
	}
	if p.mappings != nil {
		if err = p.mappings.Flush(); err != nil {
			return prepared, err
		}
	}
	var name string
	if p.tmp != nil {
		if err = p.tmp.Chmod(0400); err != nil {
			return prepared, err
		}
		if err = p.tmp.Sync(); err != nil {
			return prepared, err
		}
		name = "output.canonical-" + hex.EncodeToString(p.sum.Sum(nil)) + ".jsonl"
		if err = p.tmp.Close(); err != nil {
			return prepared, err
		}
		if err = publish(p.tmp.Name(), filepath.Join(p.dir, name), p.sum.Sum(nil)); err != nil {
			return prepared, err
		}
	}
	prepared = Prepared{p.key, name, capturedSize, prefixEnd, p.meta, p.rows}
	p.meta, p.tmp, p.mappings = nil, nil, nil
	p.closed = true
	return prepared, nil
}

// Abort releases unpublished files. It is safe after any failure or Finish.
func (p *Pack) Abort() error {
	if p.closed {
		return nil
	}
	p.closed = true
	var err error
	for _, f := range []*os.File{p.meta, p.tmp} {
		if f == nil {
			continue
		}
		if closeErr := f.Close(); !errors.Is(closeErr, os.ErrClosed) {
			err = errors.Join(err, closeErr)
		}
		if removeErr := os.Remove(f.Name()); !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
	}
	p.meta, p.tmp, p.mappings = nil, nil, nil
	return err
}

// Link publishes without replacing an existing digest name, even under a race.
func publish(tmp, final string, digest []byte) error {
	if err := os.Link(tmp, final); err == nil {
		// The complete blob is now visible; it may already have another user.
	} else if !os.IsExist(err) {
		return err
	} else {
		info, err := os.Lstat(final)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("existing pack is not a regular file")
		}
		f, err := os.Open(final)
		if err != nil {
			return err
		}
		sum := sha256.New()
		_, readErr := io.Copy(sum, f)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(readErr, syncErr, closeErr); err != nil {
			return err
		}
		if !slices.Equal(sum.Sum(nil), digest) {
			return errors.New("existing pack has conflicting contents")
		}
	}
	if err := os.Remove(tmp); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(final))
	if err == nil {
		err = errors.Join(dir.Sync(), dir.Close())
	}
	return err
}

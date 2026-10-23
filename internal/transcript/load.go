package transcript

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workeroutput"
)

// MaxTurnEvents bounds the events kept in memory for one turn.
const MaxTurnEvents = 4000

// Session is everything a conversation view shows about one subagent session.
type Session struct {
	Info    subagent.Info
	Turns   []Turn
	Since   time.Time // End of the newest omitted turn; zero means no lower bound.
	Before  time.Time // End of the selected turn window; zero means no upper bound.
	Omitted bool
	Newer   bool
}

// cachedTurn is a finished turn built from the stored record src. Any later
// write to that record (usage, end time, delivery flags, transfer labels)
// rebuilds it.
type cachedTurn struct {
	src  subagent.Turn
	turn Turn
}

type runningTurn struct {
	start, offset int64
	events        []workeroutput.Event
	omitted       bool
}

type sessionCache struct {
	mu           sync.Mutex
	done         map[int]cachedTurn
	running      map[int]runningTurn
	dataDir, key string
}

// Loader reads sessions incrementally and permits concurrent loads across sessions.
type Loader struct {
	Store  *subagent.Store
	mu     sync.Mutex
	caches map[string]*sessionCache
}

func NewLoader(store *subagent.Store) *Loader {
	if store == nil {
		store = subagent.DefaultStore()
	}
	return &Loader{Store: store, caches: map[string]*sessionCache{}}
}

func (l *Loader) cache(id string) *sessionCache {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.caches[id]
	if c == nil {
		c = &sessionCache{done: map[int]cachedTurn{}, running: map[int]runningTurn{}, dataDir: filepath.Dir(l.Store.Root), key: "sessions/" + id + "/agents/1"}
		l.caches[id] = c
	}
	return c
}

// read returns a turn's normalized events. They end at the turn's recorded
// end, else where the following turn starts: a turn that ended without output
// has no recorded end. Only the newest turn reads to the end of the file.
func (c *sessionCache) read(path string, t subagent.Turn, following *subagent.Turn) ([]workeroutput.Event, bool) {
	if t.Done() {
		defer delete(c.running, t.N)
	}
	end, bounded := t.NormEnd, t.NormEnd > 0
	if !bounded && following != nil {
		end, bounded = following.NormStart, true
	}
	r, ok := c.running[t.N]
	if !ok || r.start != t.NormStart || fileSize(path) < r.offset || (bounded && end < r.offset) {
		r = runningTurn{start: t.NormStart, offset: t.NormStart}
	}
	limit := int64(0)
	if bounded {
		limit = end - r.offset
		if limit <= 0 {
			return r.events, r.omitted
		}
	}
	events, next, err := runstore.ReadProjectedNormalizedOutputTailPath(c.dataDir, c.key, path, r.offset, limit, MaxTurnEvents+1)
	if err != nil {
		return nil, false
	}
	events = append(r.events, events...)
	// The reader keeps compaction records, so this only trims the others.
	if len(events) > MaxTurnEvents {
		events = workeroutput.KeepCompactions(append([]workeroutput.Event(nil), events...), MaxTurnEvents)
		r.omitted = true
	}
	r.events, r.offset = events, next
	if !t.Done() {
		c.running[t.N] = r
	}
	return r.events, r.omitted
}

// LoadTurn reads one stored turn, including an older or closed session's turn.
// It does not read the other turns' events.
func (l *Loader) LoadTurn(ref string, n int) (Session, error) {
	info, err := l.Store.ResolveSnapshot(ref)
	if err != nil {
		return Session{}, err
	}
	c := l.cache(info.ID)
	c.mu.Lock()
	defer c.mu.Unlock()
	t, err := l.Store.ReadTurn(info.ID, n)
	if err != nil {
		return Session{}, err
	}
	path, err := l.Store.NormalizedPath(info.ID)
	if err != nil {
		return Session{}, err
	}
	turns, err := l.Store.Turns(info.ID)
	if err != nil {
		return Session{}, err
	}
	s := Session{Info: info, Before: t.Ended}
	var following *subagent.Turn
	for i, turn := range turns {
		if turn.N == n {
			if i > 0 {
				s.Since = turns[i-1].Ended
			}
			if i+1 < len(turns) {
				following = &turns[i+1]
			}
			break
		}
	}
	events, omitted := c.read(path, t, following)
	s.Turns = []Turn{SessionTurn(t, events, omitted)}
	return s, nil
}

// Load reads a session. lastTurns > 0 keeps only the newest turns.
func (l *Loader) Load(ref string, lastTurns int) (Session, error) {
	return l.load(ref, lastTurns, 0, false)
}

// LoadPage reads at most limit turns before the exclusive turn number.
// A zero cursor selects the newest turns. Only selected turns read events.
func (l *Loader) LoadPage(ref string, before, limit int) (Session, error) {
	return l.load(ref, limit, before, false)
}

// LoadAfter reads turns after an exclusive turn number. A zero cursor starts
// at the oldest turn. It shares decoding and page cache limits with LoadPage.
func (l *Loader) LoadAfter(ref string, after, limit int) (Session, error) {
	return l.load(ref, limit, after, true)
}

func (l *Loader) load(ref string, lastTurns, cursor int, forward bool) (Session, error) {
	info, err := l.Store.ResolveSnapshot(ref)
	if err != nil {
		return Session{}, err
	}
	c := l.cache(info.ID)
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Session{Info: info}
	turns, err := l.Store.Turns(info.ID)
	if err != nil {
		return s, err
	}
	start, end := 0, len(turns)
	if cursor > 0 {
		index := -1
		for i, turn := range turns {
			if turn.N == cursor {
				index = i
				break
			}
		}
		if index < 0 {
			return s, fmt.Errorf("unknown turn cursor %d", cursor)
		}
		if forward {
			start = index + 1
		} else {
			end = index
		}
	}
	if lastTurns > 0 {
		if forward {
			end = min(end, start+lastTurns)
		} else {
			start = max(start, end-lastTurns)
		}
	}
	if start > 0 {
		s.Since = turns[start-1].Ended
		s.Omitted = true
	}
	s.Newer = end < len(turns)
	if s.Newer && end > start {
		s.Before = turns[end-1].Ended
	}
	cache := c.done
	// Reuse selected turns, then release events from pages outside this window.
	c.done = make(map[int]cachedTurn, end-start)
	path, _ := l.Store.NormalizedPath(info.ID)
	for i := start; i < end; i++ {
		t := turns[i]
		if cached, ok := cache[t.N]; ok && t.Done() && reflect.DeepEqual(cached.src, t) {
			c.done[t.N] = cached
			s.Turns = append(s.Turns, cached.turn)
			continue
		}
		var following *subagent.Turn
		if i+1 < len(turns) {
			following = &turns[i+1]
		}
		events, omitted := c.read(path, t, following)
		ct := SessionTurn(t, events, omitted)
		if t.Done() {
			c.done[t.N] = cachedTurn{src: t, turn: ct}
		}
		s.Turns = append(s.Turns, ct)
	}
	return s, nil
}

func fileSize(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return 0
}

// RawTail returns the last max bytes of the session's raw harness stdout.
func RawTail(store *subagent.Store, id string, max int64) (string, error) {
	path, err := store.RawOutputPath(id)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	off := st.Size() - max
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return "", err
	}
	return string(buf), nil
}

package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	// MaxJarBytes caps the serialized jar.json size on disk.
	MaxJarBytes = 5 << 20
	// MaxJarCookies caps the number of live cookies stored in one jar.
	MaxJarCookies = 10_000
	// MaxSyncRequestBytes bounds one POST /v1/browser/sync HTTP request body.
	MaxSyncRequestBytes = 6 << 20
	// TombstoneRetention is how long delete tombstones remain in the jar before pruning.
	TombstoneRetention = 7 * 24 * time.Hour
)

// ErrJarFull reports that applying a push would exceed the cookie count or byte size cap.
var ErrJarFull = errors.New("cookie jar limit exceeded")

// SyncRequest is the body of POST /v1/browser/sync.
type SyncRequest struct {
	Profile     string      `json:"profile,omitempty"`
	BaseVersion int64       `json:"baseVersion"`
	Upserts     []Cookie    `json:"upserts,omitempty"`
	Deletes     []CookieKey `json:"deletes,omitempty"`
}

// SyncResponse is returned by GET and POST /v1/browser/sync.
type SyncResponse struct {
	Profile string      `json:"profile"`
	Version int64       `json:"version"`
	Upserts []Cookie    `json:"upserts,omitempty"`
	Deletes []CookieKey `json:"deletes,omitempty"`
}

// JarCookie is one live cookie entry stored in jar.json.
type JarCookie struct {
	Key     CookieKey `json:"key"`
	Version int64     `json:"version"`
	Cookie  Cookie    `json:"cookie"`
}

// JarTombstone records a deleted cookie key and when it was deleted.
type JarTombstone struct {
	Key       CookieKey `json:"key"`
	Version   int64     `json:"version"`
	DeletedAt time.Time `json:"deletedAt"`
}

// Jar is the on-disk and in-memory state of one shared browser profile's cookie jar.
type Jar struct {
	Version    int64
	cookies    map[KeyID]JarCookie
	tombstones map[KeyID]JarTombstone
}

type jarDisk struct {
	Version    int64          `json:"version"`
	Cookies    []JarCookie    `json:"cookies"`
	Tombstones []JarTombstone `json:"tombstones,omitempty"`
}

// NewJar returns an empty cookie jar at version 0.
func NewJar() *Jar {
	return &Jar{
		cookies:    map[KeyID]JarCookie{},
		tombstones: map[KeyID]JarTombstone{},
	}
}

// NewJarFromCookies builds an initial jar from extracted cookies (e.g. v1 migration),
// dropping already-expired cookies and enforcing the count and size caps.
func NewJarFromCookies(cookies []Cookie, now time.Time) (*Jar, []byte, error) {
	j := NewJar()
	for _, c := range cookies {
		if c.ExpiredAt(now) {
			continue
		}
		j.cookies[c.Key.ID()] = JarCookie{
			Key:     c.Key,
			Version: 1,
			Cookie:  c,
		}
	}
	if len(j.cookies) > 0 {
		j.Version = 1
	}
	if len(j.cookies) > MaxJarCookies {
		return nil, nil, fmt.Errorf("%w: exceeds %d cookie limit", ErrJarFull, MaxJarCookies)
	}
	encoded, err := j.MarshalJSON()
	if err != nil {
		return nil, nil, err
	}
	if len(encoded) > MaxJarBytes {
		return nil, nil, fmt.Errorf("%w: exceeds %d byte limit", ErrJarFull, MaxJarBytes)
	}
	return j, encoded, nil
}

// LiveCount returns the number of non-expired live cookies in the jar at time now.
func (j *Jar) LiveCount(now time.Time) int {
	if j == nil {
		return 0
	}
	n := 0
	for _, entry := range j.cookies {
		if !entry.Cookie.ExpiredAt(now) {
			n++
		}
	}
	return n
}

// Cookies returns a sorted copy of all live cookies currently in the jar.
func (j *Jar) Cookies() []JarCookie {
	if j == nil {
		return nil
	}
	out := make([]JarCookie, 0, len(j.cookies))
	for _, entry := range j.cookies {
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b JarCookie) int {
		return compareKeyID(a.Key.ID(), b.Key.ID())
	})
	return out
}

// Tombstones returns a sorted copy of all tombstones currently in the jar.
func (j *Jar) Tombstones() []JarTombstone {
	if j == nil {
		return nil
	}
	out := make([]JarTombstone, 0, len(j.tombstones))
	for _, entry := range j.tombstones {
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b JarTombstone) int {
		return compareKeyID(a.Key.ID(), b.Key.ID())
	})
	return out
}

// MarshalJSON encodes the jar deterministically for disk storage.
func (j *Jar) MarshalJSON() ([]byte, error) {
	disk := jarDisk{
		Version:    j.Version,
		Cookies:    j.Cookies(),
		Tombstones: j.Tombstones(),
	}
	if disk.Cookies == nil {
		disk.Cookies = []JarCookie{}
	}
	return json.Marshal(disk)
}

// LoadJar reads a jar from path. It returns (nil, false, nil) if path does not exist.
func LoadJar(path string) (*Jar, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var disk jarDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, false, errors.New("invalid cookie jar file")
	}
	j := &Jar{
		Version:    disk.Version,
		cookies:    make(map[KeyID]JarCookie, len(disk.Cookies)),
		tombstones: make(map[KeyID]JarTombstone, len(disk.Tombstones)),
	}
	for _, entry := range disk.Cookies {
		// Re-derive normalized key from the validated cookie.
		entry.Key = entry.Cookie.Key
		j.cookies[entry.Key.ID()] = entry
	}
	for _, ts := range disk.Tombstones {
		k, err := NormalizeKey(ts.Key)
		if err != nil {
			return nil, false, errors.New("invalid tombstone in cookie jar file")
		}
		ts.Key = k
		if _, live := j.cookies[k.ID()]; !live {
			j.tombstones[k.ID()] = ts
		}
	}
	return j, true, nil
}

// SaveJarBytes writes encoded jar JSON atomically to path with mode 0600.
func SaveJarBytes(path string, encoded []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".jar-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// ValidateSyncRequest validates request fields once at the API boundary without exposing cookie values.
func ValidateSyncRequest(req *SyncRequest) error {
	if req.BaseVersion < 0 {
		return errors.New("baseVersion must be non-negative")
	}
	for i := range req.Deletes {
		k, err := NormalizeKey(req.Deletes[i])
		if err != nil {
			return fmt.Errorf("delete key %d: %w", i, err)
		}
		req.Deletes[i] = k
	}
	return nil
}

// Sync computes the next jar state and sync response for req at time now without mutating j.
// It drops expired cookies and tombstones older than 7 days, collects changes since req.BaseVersion
// (excluding keys overwritten or deleted by req itself), applies req with last-writer-wins,
// and enforces MaxJarCookies and MaxJarBytes before returning.
func (j *Jar) Sync(now time.Time, req SyncRequest) (next *Jar, resp SyncResponse, encoded []byte, changed bool, err error) {
	if j == nil {
		j = NewJar()
	}
	next = &Jar{
		Version:    j.Version,
		cookies:    make(map[KeyID]JarCookie, len(j.cookies)+len(req.Upserts)),
		tombstones: make(map[KeyID]JarTombstone, len(j.tombstones)+len(req.Deletes)),
	}

	// Copy while dropping expired cookies and tombstones older than TombstoneRetention.
	cutoff := now.Add(-TombstoneRetention)
	for id, entry := range j.cookies {
		if entry.Cookie.ExpiredAt(now) {
			changed = true
			continue
		}
		next.cookies[id] = entry
	}
	for id, ts := range j.tombstones {
		if !ts.DeletedAt.IsZero() && ts.DeletedAt.Before(cutoff) {
			changed = true
			continue
		}
		next.tombstones[id] = ts
	}

	// Track keys this request is writing so we do not echo them back to the caller.
	pushed := make(map[KeyID]struct{}, len(req.Upserts)+len(req.Deletes))
	for _, c := range req.Upserts {
		if !c.ExpiredAt(now) {
			pushed[c.Key.ID()] = struct{}{}
		}
	}
	for _, k := range req.Deletes {
		pushed[k.ID()] = struct{}{}
	}

	// Collect changes since BaseVersion before applying this caller's pushes.
	// If BaseVersion is 0 (initial pull) or ahead of the jar version, send the full jar and active tombstones.
	base := req.BaseVersion
	if base > next.Version {
		base = 0
	}
	for id, entry := range next.cookies {
		if _, own := pushed[id]; own {
			continue
		}
		if base == 0 || entry.Version > base {
			resp.Upserts = append(resp.Upserts, entry.Cookie)
		}
	}
	for id, ts := range next.tombstones {
		if _, own := pushed[id]; own {
			continue
		}
		if base == 0 || ts.Version > base {
			resp.Deletes = append(resp.Deletes, ts.Key)
		}
	}
	slices.SortFunc(resp.Upserts, func(a, b Cookie) int {
		return compareKeyID(a.Key.ID(), b.Key.ID())
	})
	slices.SortFunc(resp.Deletes, func(a, b CookieKey) int {
		return compareKeyID(a.ID(), b.ID())
	})

	// Apply caller's pushes (last writer wins). Only bump version if state actually changes.
	nextVer := next.Version + 1
	pushedChange := false
	for _, c := range req.Upserts {
		if c.ExpiredAt(now) {
			continue
		}
		id := c.Key.ID()
		if existing, ok := next.cookies[id]; ok && existing.Cookie.Equal(c) {
			continue
		}
		delete(next.tombstones, id)
		next.cookies[id] = JarCookie{
			Key:     c.Key,
			Version: nextVer,
			Cookie:  c,
		}
		pushedChange = true
	}
	for _, k := range req.Deletes {
		id := k.ID()
		_, wasLive := next.cookies[id]
		_, alreadyDeleted := next.tombstones[id]
		if !wasLive && alreadyDeleted {
			continue
		}
		delete(next.cookies, id)
		next.tombstones[id] = JarTombstone{
			Key:       k,
			Version:   nextVer,
			DeletedAt: now.UTC(),
		}
		pushedChange = true
	}
	if pushedChange {
		next.Version = nextVer
		changed = true
	}

	// Enforce cookie count and serialized size caps when state changed.
	if len(next.cookies) > MaxJarCookies {
		return nil, SyncResponse{}, nil, false, fmt.Errorf("%w: exceeds %d cookie limit", ErrJarFull, MaxJarCookies)
	}
	if changed {
		encoded, err = next.MarshalJSON()
		if err != nil {
			return nil, SyncResponse{}, nil, false, err
		}
		if len(encoded) > MaxJarBytes {
			return nil, SyncResponse{}, nil, false, fmt.Errorf("%w: exceeds %d byte limit", ErrJarFull, MaxJarBytes)
		}
	}
	resp.Version = next.Version
	return next, resp, encoded, changed, nil
}

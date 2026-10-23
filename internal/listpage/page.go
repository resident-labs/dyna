// Package listpage selects open work and a bounded window of closed work.
package listpage

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

const Recent = 24 * time.Hour
const DefaultLimit = 50
const MaxLimit = 200

type Page[T any] struct {
	Items  []T    `json:"items"`
	Older  bool   `json:"older"`
	Cursor string `json:"cursor"`
}
type Meta struct {
	ID   string
	At   time.Time
	Open bool
}
type cursor struct {
	At time.Time
	ID string
}

// boundaryOf reads a page cursor. An empty before names no boundary yet.
func boundaryOf(before string) (cursor, error) {
	if before == "" {
		return cursor{}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(before)
	if err != nil {
		return cursor{}, fmt.Errorf("invalid list cursor")
	}
	var c cursor
	if err := json.Unmarshal(data, &c); err != nil || (c.At.IsZero() && c.ID == "") {
		return cursor{}, fmt.Errorf("invalid list cursor")
	}
	return c, nil
}

// Boundary reads a page cursor: the time and ID of the last closed item that
// page returned. A store that bounds the rows of a later page in SQL uses it,
// so the page it reads matches what Select would have selected.
func Boundary(before string) (time.Time, string, error) {
	c, err := boundaryOf(before)
	return c.At, c.ID, err
}

func encodeCursor(c cursor) string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}

// Select always includes open items on the first page. Closed items use a
// 24-hour cutoff and a limit. Later pages contain closed items only, ordered
// by descending time and ascending ID. The cursor fixes the page boundary.
func Select[T any](items []T, before string, limit int, now time.Time, meta func(T) Meta) (Page[T], error) {
	out, _, _, err := selectPage(items, before, limit, now, meta)
	return out, err
}

// SelectWindow is Select for a store that read only the closed items a page
// can reach, and reported with below whether closed items remain under that
// window. A later page that showed no closed item reports no older page, so a
// client never asks for the same cursor twice.
func SelectWindow[T any](items []T, before string, limit int, now time.Time, meta func(T) Meta, below bool) (Page[T], error) {
	out, boundary, advanced, err := selectPage(items, before, limit, now, meta)
	if err != nil || out.Older || !below {
		return out, err
	}
	if before != "" && !advanced {
		return out, nil
	}
	out.Older, out.Cursor = true, encodeCursor(boundary)
	return out, nil
}

// selectPage also reports whether a closed item advanced the boundary, so a
// caller with a bounded window can tell a page that showed closed work from
// one that could not.
func selectPage[T any](items []T, before string, limit int, now time.Time, meta func(T) Meta) (Page[T], cursor, bool, error) {
	out := Page[T]{Items: []T{}}
	limit = Limit(limit)
	boundary, err := boundaryOf(before)
	if err != nil {
		return out, boundary, false, err
	}
	if before == "" {
		boundary = cursor{At: now.Add(-Recent)}
	}
	// Only the newest limit+1 closed items can reach the page, and the extra
	// one says whether older ones remain. A bounded insertion keeps each of
	// thousands of items at one comparison against the oldest kept item.
	type entry struct {
		item T
		meta Meta
	}
	newer := func(a, b Meta) bool {
		if !a.At.Equal(b.At) {
			return a.At.After(b.At)
		}
		return a.ID < b.ID
	}
	closed := make([]entry, 0, limit+1)
	aged := false // a closed item is older than the first-page cutoff
	for _, item := range items {
		m := meta(item)
		if m.Open {
			if before == "" {
				out.Items = append(out.Items, item)
			}
			continue
		}
		if before != "" && (m.At.After(boundary.At) || (m.At.Equal(boundary.At) && m.ID <= boundary.ID)) {
			continue
		}
		// The first page never shows these, so they need no sort.
		if before == "" && m.At.Before(boundary.At) {
			aged = true
			continue
		}
		if len(closed) == limit+1 {
			if !newer(m, closed[limit].meta) {
				continue
			}
			closed = closed[:limit]
		}
		at := sort.Search(len(closed), func(i int) bool { return newer(m, closed[i].meta) })
		closed = append(closed, entry{})
		copy(closed[at+1:], closed[at:])
		closed[at] = entry{item, m}
	}
	count := 0
	for _, e := range closed {
		if count == limit {
			out.Older = true
			break
		}
		out.Items = append(out.Items, e.item)
		count++
		boundary = cursor{At: e.meta.At, ID: e.meta.ID}
	}
	if aged {
		out.Older = true
	}
	if out.Older {
		out.Cursor = encodeCursor(boundary)
	}
	return out, boundary, count > 0, nil
}

// Limit applies the same default and maximum to each history request.
func Limit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	return min(limit, MaxLimit)
}

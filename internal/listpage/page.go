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

// Select always includes open items on the first page. Closed items use a
// 24-hour cutoff and a limit. Later pages contain closed items only, ordered
// by descending time and ascending ID. The cursor fixes the page boundary.
func Select[T any](items []T, before string, limit int, now time.Time, meta func(T) Meta) (Page[T], error) {
	out := Page[T]{Items: []T{}}
	limit = Limit(limit)
	boundary := cursor{At: now.Add(-Recent)}
	if before != "" {
		boundary = cursor{}
		data, err := base64.RawURLEncoding.DecodeString(before)
		if err != nil {
			return out, fmt.Errorf("invalid list cursor")
		}
		if err := json.Unmarshal(data, &boundary); err != nil || (boundary.At.IsZero() && boundary.ID == "") {
			return out, fmt.Errorf("invalid list cursor")
		}
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
		data, _ := json.Marshal(boundary)
		out.Cursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return out, nil
}

// Limit applies the same default and maximum to each history request.
func Limit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	return min(limit, MaxLimit)
}

// Package lanesync fetches local lane remotes and records quiet sync status.
package lanesync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/gitexec"
	"dyna-agent/internal/notices"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
)

type Remote struct {
	Ahead     int       `json:"ahead"`
	Behind    int       `json:"behind"`
	Head      string    `json:"remoteHead"`
	Branch    string    `json:"branch"`
	CheckedAt time.Time `json:"checkedAt"`
	LastError string    `json:"lastError,omitempty"`
	Rewrite   string    `json:"rewrite,omitempty"`
}
type RemoteStatus map[string]map[string]Remote
type Snapshot struct {
	Repositories RemoteStatus         `json:"repositories"`
	Events       map[string]string    `json:"events"`
	Operations   map[string]Operation `json:"operations,omitempty"`
	Exports      map[string]Export    `json:"exports,omitempty"`
}
type Service struct {
	Dir string
	// git runs one Git read; nil runs read. Tests count the calls through it.
	git func(context.Context, string, ...string) (string, error)

	mu sync.Mutex
	// views keeps one repository's Git view while its refs and export stay
	// the same. Status is a web read, so it must not spawn Git per call.
	views map[string]*repoView
}

// fetchMu is an operation lease shared by the engine timer and fetch calls.
// It serializes Git work; snapshot reads and writes never wait on it.
var fetchMu sync.Mutex

func New() *Service { return &Service{Dir: store.DataDir()} }

// fetchEvery is how often the engine fetches local lane remotes.
const fetchEvery = 5 * time.Minute

// Tick fetches local lane remotes and returns when the next fetch is due,
// counted from now. Fetches share an operation lease.
func (s *Service) Tick(ctx context.Context, now time.Time) (time.Time, error) {
	next := now.Add(fetchEvery)
	v, e := settings.Load("")
	if e != nil {
		return next, e
	}
	return next, s.Fetch(ctx, v.LocalLaneRepos)
}
func read(ctx context.Context, repo string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b, e := gitexec.Command(ctx, repo, args...).Output()
	if e != nil {
		return "", fmt.Errorf("git %s: %w", args[0], e)
	}
	return strings.TrimSpace(string(b)), nil
}
func ancestor(ctx context.Context, repo, a, b string) (bool, error) {
	_, e := read(ctx, repo, "merge-base", "--is-ancestor", a, b)
	if e == nil {
		return true, nil
	}
	var exit interface{ ExitCode() int }
	if errors.As(e, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, e
}
func disagreementKey(repo, a, b string) string {
	if a > b {
		a, b = b, a
	}
	return "sync:disagreement:" + repo + ":" + a + ":" + b
}
func (s *Service) Fetch(ctx context.Context, repos []string) error {
	fetchMu.Lock()
	defer fetchMu.Unlock()
	previous, err := s.Read()
	if err != nil {
		return err
	}
	var failures []error
	events := map[string]string{}
	result := RemoteStatus{}
	for _, repo := range repos {
		oldRemotes := previous.Repositories[repo]
		remotes := map[string]Remote{}
		result[repo] = remotes
		names, e := read(ctx, repo, "remote")
		if e != nil {
			failures = append(failures, e)
			result[repo] = oldRemotes
			for name, r := range oldRemotes {
				r.CheckedAt = time.Now().UTC()
				r.LastError = e.Error()
				oldRemotes[name] = r
			}
			for key, event := range previous.Events {
				if strings.HasPrefix(key, "sync:rewrite:"+repo+":") || strings.HasPrefix(key, "sync:disagreement:"+repo+":") {
					events[key] = event
				}
			}
			continue
		}
		base := "refs/heads/dyna/integration"
		if _, e := read(ctx, repo, "rev-parse", "--verify", base); e != nil {
			base = "refs/heads/main"
		}
		for _, name := range strings.Fields(names) {
			old := oldRemotes[name]
			current := old
			current.CheckedAt = time.Now().UTC()
			current.LastError = ""
			if e := fetch(ctx, repo, name); e != nil {
				current.LastError = "fetch failed: " + e.Error()
				remotes[name] = current
				if old.Rewrite != "" {
					events["sync:rewrite:"+repo+":"+name] = old.Rewrite
				}
				continue
			}
			branch, e := read(ctx, repo, "symbolic-ref", "refs/remotes/"+name+"/HEAD")
			if e != nil {
				branch = "refs/remotes/" + name + "/main"
			}
			head, e := read(ctx, repo, "rev-parse", "--verify", branch)
			if e == nil {
				current.Branch = branch
				current.Head = head
				if old.Head != "" && old.Head != head {
					ok, err := ancestor(ctx, repo, old.Head, head)
					if err != nil {
						e = err
					} else if !ok {
						current.Rewrite = old.Head + " -> " + head
					} else {
						current.Rewrite = ""
					}
				}
			}
			if e == nil {
				counts, err := read(ctx, repo, "rev-list", "--left-right", "--count", base+"..."+head)
				e = err
				if e == nil {
					fields := strings.Fields(counts)
					if len(fields) != 2 {
						e = fmt.Errorf("invalid ahead/behind counts")
					} else {
						current.Ahead, _ = strconv.Atoi(fields[0])
						current.Behind, _ = strconv.Atoi(fields[1])
					}
				}
			}
			if e != nil {
				current.LastError = e.Error()
			}
			if current.Rewrite != "" {
				events["sync:rewrite:"+repo+":"+name] = current.Rewrite
			}
			remotes[name] = current
		}
		sorted := make([]string, 0, len(remotes))
		for name := range remotes {
			sorted = append(sorted, name)
		}
		sort.Strings(sorted)
		for i, a := range sorted {
			for _, b := range sorted[i+1:] {
				ra, rb := remotes[a], remotes[b]
				if ra.LastError != "" || rb.LastError != "" {
					key := disagreementKey(repo, a, b)
					if old := previous.Events[key]; old != "" {
						events[key] = old
					}
					continue
				}
				if ra.Head == "" || rb.Head == "" {
					continue
				}
				ab, ea := ancestor(ctx, repo, ra.Head, rb.Head)
				ba, eb := ancestor(ctx, repo, rb.Head, ra.Head)
				if ea != nil || eb != nil {
					failures = append(failures, ea, eb)
					continue
				}
				if !ab && !ba {
					events[disagreementKey(repo, a, b)] = ra.Head + " / " + rb.Head
				}
			}
		}
	}
	ns := notices.Store{Dir: s.Dir}
	for key, event := range events {
		if previous.Events[key] != event {
			if _, e := ns.Post(notices.Notice{Key: key, Kind: notices.KindSync, Severity: "warn", Text: "Remote history split: " + key + " (" + event + "). Review sync status."}); e != nil {
				return e
			}
		}
	}
	list, e := ns.List()
	if e != nil {
		return e
	}
	for _, n := range list {
		if n.Kind == notices.KindSync && events[n.Key] == "" {
			if e := ns.Dismiss(n.ID); e != nil {
				return e
			}
		}
	}
	// Fetches and notice writes finish before the short SQL transaction.
	// Keep operation and export changes made while Git was running.
	err = s.update(ctx, func(st *Snapshot) error {
		st.Repositories = result
		st.Events = events
		return nil
	})
	// Save successful results even when a repository cannot be inspected.
	return errors.Join(append(failures, err)...)
}

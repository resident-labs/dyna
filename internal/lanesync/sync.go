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
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
)

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

// syncNotice is the typed owner notice of one sync event: a split between two
// lanes, or a lane whose history was rewritten.
func syncNotice(key, repo, lane, other string, rewritten bool) notices.Notice {
	return notices.Notice{
		Key:      key,
		Kind:     notices.KindSync,
		Severity: notices.SeverityWarn,
		Sync:     &notices.SyncFacts{Repo: repo, Lane: lane, Other: other, Rewritten: rewritten},
		Command:  "dyna git sync status",
	}
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
	// values is what this pass saw per event key, so a later pass posts only a
	// change. found holds the typed notice each key posts.
	values := map[string]string{}
	found := map[string]notices.Notice{}
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
			// A repository that cannot be inspected keeps the events the last
			// pass found, so its notice stays until sync recovers.
			for key, event := range previous.Events {
				switch {
				case strings.HasPrefix(key, "sync:rewrite:"+repo+":"):
					name := strings.TrimPrefix(key, "sync:rewrite:"+repo+":")
					values[key] = event
					found[key] = syncNotice(key, repo, name, "", true)
				case strings.HasPrefix(key, "sync:disagreement:"+repo+":"):
					values[key] = event
					found[key] = syncNotice(key, repo, "", "", false)
				}
			}
			continue
		}
		base := "refs/heads/dyna/integration"
		var baseErr error
		if _, e := read(ctx, repo, "rev-parse", "--verify", base); e != nil {
			resolved, err := repoid.DefaultBranch(ctx, repo, read)
			base, baseErr = "refs/heads/"+resolved.Name, err
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
					key := "sync:rewrite:" + repo + ":" + name
					values[key] = old.Rewrite
					found[key] = syncNotice(key, repo, name, "", true)
				}
				continue
			}
			branch, e := read(ctx, repo, "symbolic-ref", "refs/remotes/"+name+"/HEAD")
			if e != nil {
				resolved, resolveErr := repoid.DefaultBranch(ctx, repo, read)
				if resolveErr != nil {
					e = resolveErr
				} else {
					branch = "refs/remotes/" + name + "/" + resolved.Name
					e = nil
				}
			}
			head := ""
			if e == nil {
				head, e = read(ctx, repo, "rev-parse", "--verify", branch)
			}
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
			if e == nil && baseErr != nil {
				e = baseErr
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
				key := "sync:rewrite:" + repo + ":" + name
				values[key] = current.Rewrite
				found[key] = syncNotice(key, repo, name, "", true)
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
						values[key] = old
						found[key] = syncNotice(key, repo, a, b, false)
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
					key := disagreementKey(repo, a, b)
					values[key] = ra.Head + " / " + rb.Head
					found[key] = syncNotice(key, repo, a, b, false)
				}
			}
		}
	}
	ns := notices.Store{Dir: s.Dir}
	for key, n := range found {
		if previous.Events[key] != values[key] {
			if _, e := ns.Post(n); e != nil {
				return e
			}
		}
	}
	list, e := ns.List()
	if e != nil {
		return e
	}
	for _, n := range list {
		if _, live := found[n.Key]; n.Kind == notices.KindSync && !live {
			if e := ns.Dismiss(n.ID); e != nil {
				return e
			}
		}
	}
	// Fetches and notice writes finish before the short SQL transaction.
	// Keep operation and export changes made while Git was running.
	err = s.update(ctx, func(st *Snapshot) error {
		st.Repositories = result
		st.Events = values
		return nil
	})
	// Save successful results even when a repository cannot be inspected.
	return errors.Join(append(failures, err)...)
}

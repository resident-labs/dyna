package watch

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// ghQuotaEvery is how often the engine writes its quota summary to the journal.
const ghQuotaEvery = 10 * time.Minute

// ghQuotaLog counts the GitHub requests the engine makes, by caller. The REST
// quota is 5,000 an hour and the engine shares it with every agent, so knowing
// which path spends it matters. A conditional request that GitHub answers with
// 304 costs no quota, so free replies are counted apart.
type ghQuotaLog struct {
	mu      sync.Mutex
	since   time.Time
	counted map[string]int
	free    map[string]int
	now     func() time.Time
	logf    func(format string, args ...any)
}

// engineGHQuota is shared by every engine request. Tests replace it.
var engineGHQuota = &ghQuotaLog{}

// record adds one completed request and writes a summary every ghQuotaEvery.
func (q *ghQuotaLog) record(path string, notModified bool) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	if q.now != nil {
		now = q.now()
	}
	if q.since.IsZero() {
		q.since = now
	}
	if q.counted == nil {
		q.counted, q.free = map[string]int{}, map[string]int{}
	}
	caller := ghQuotaCaller(path)
	if notModified {
		q.free[caller]++
	} else {
		q.counted[caller]++
	}
	if now.Sub(q.since) < ghQuotaEvery {
		return
	}
	q.summary(now)
}

// summary logs one line and starts a new window. Callers hold the lock.
func (q *ghQuotaLog) summary(now time.Time) {
	counted, free := 0, 0
	for _, n := range q.counted {
		counted += n
	}
	for _, n := range q.free {
		free += n
	}
	logf := q.logf
	if logf == nil {
		logf = log.Printf
	}
	logf("GitHub quota: %d counted and %d free (304) requests in %s%s", counted, free, now.Sub(q.since).Round(time.Minute), countedCallers(q.counted))
	q.counted, q.free, q.since = map[string]int{}, map[string]int{}, now
}

// countedCallers names the callers that spend quota, largest first.
func countedCallers(counted map[string]int) string {
	type row struct {
		name  string
		count int
	}
	rows := make([]row, 0, len(counted))
	for name, count := range counted {
		rows = append(rows, row{name, count})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].name < rows[j].name
	})
	if len(rows) > 6 {
		rows = rows[:6]
	}
	var parts []string
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s %d", r.name, r.count))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; counted by caller: " + strings.Join(parts, ", ")
}

// ghQuotaCaller names the request class of one GitHub API path.
func ghQuotaCaller(path string) string {
	if path == "graphql" {
		return "graphql"
	}
	endpoint, _, _ := strings.Cut(path, "?")
	rest, ok := strings.CutPrefix(endpoint, "repos/")
	if !ok {
		return "other"
	}
	// Drop the owner and repository, which name the same callers everywhere.
	if _, after, found := strings.Cut(rest, "/"); found {
		if _, tail, found := strings.Cut(after, "/"); found {
			rest = tail
		}
	}
	switch {
	case strings.HasPrefix(rest, "commits/"):
		if strings.HasSuffix(rest, "/check-runs") {
			return "check-runs"
		}
		if strings.HasSuffix(rest, "/statuses") {
			return "statuses"
		}
		return "commit"
	case strings.HasPrefix(rest, "actions/jobs/"):
		return "job-log"
	case strings.HasPrefix(rest, "actions/workflows"):
		return "workflows"
	case strings.HasPrefix(rest, "actions/runs/"):
		switch {
		case strings.HasSuffix(rest, "/jobs"):
			return "run-jobs"
		case strings.HasSuffix(rest, "/artifacts"):
			return "artifacts"
		case strings.HasSuffix(rest, "/cancel"):
			return "run-cancel"
		case strings.HasSuffix(rest, "/rerun"):
			return "run-rerun"
		}
		return "run"
	case rest == "actions/runs":
		return "runs"
	case strings.HasPrefix(rest, "contents/"):
		return "contents"
	case rest == "statuses":
		return "status-write"
	case rest == "pulls":
		return "pulls"
	case strings.HasPrefix(rest, "issues/"):
		return "comments"
	case strings.HasPrefix(rest, "pulls/"):
		// pulls/{number}/{kind}: the number names the same caller everywhere.
		_, tail, found := strings.Cut(strings.TrimPrefix(rest, "pulls/"), "/")
		if !found {
			return "pull"
		}
		kind, _, _ := strings.Cut(tail, "/")
		switch kind {
		case "comments", "reviews", "files":
			return kind
		}
		return "pull"
	}
	return "other"
}

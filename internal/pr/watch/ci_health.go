package watch

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/cibudget"
	"dyna-agent/internal/cihealth"
	"dyna-agent/internal/library"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
)

// slowTestRepeatGap separates two breach episodes. One loaded host breaches
// many runs within the same minutes and flags whatever happens to be running;
// recurrence an hour apart is a slow test, not a spike.
const slowTestRepeatGap = time.Hour

// maxStoredTests bounds one run's rows: every test over the limit, then the
// slowest tests under it, so a test on its way up still shows.
const (
	maxStoredTests = 200
	slowestUnder   = 10
)

// ciHealth keeps the rows beside this watcher's own state, in dyna.db.
func (s *Service) ciHealth() cihealth.Store {
	root := ""
	if s.Store != nil {
		root = filepath.Dir(s.Store.Root)
	}
	if root == "" || root == "." {
		root = store.DataDir()
	}
	return cihealth.Store{DataDir: root}
}

// recordTestHealth saves the run's near-limit tests and returns the tests
// that broke the limit, which the crossing line then judges. A store problem
// is logged: it must never change a CI verdict.
func (s *Service) recordTestHealth(ctx context.Context, r Record, run Run, tests []cibudget.Test, limit time.Duration) []string {
	started, err := time.Parse(time.RFC3339, run.Started)
	if err != nil || started.IsZero() {
		log.Printf("PR #%d run %s: CI health needs the run start time: %v", r.Number, runToken(run), err)
		return nil
	}
	stored := recordableTests(tests, limit)
	if len(stored) == 0 {
		// Nothing worth a row: no confirmed breach, and no report to show.
		return nil
	}
	rows := make([]cihealth.Test, 0, len(stored))
	var breached []string
	for _, test := range stored {
		id := Sanitize(test.ID())
		rows = append(rows, cihealth.Test{
			Room: r.RoomID, Test: id, Package: test.Package, Token: runToken(run),
			PR: r.Number, Head: run.HeadSHA, Started: started, Duration: test.Duration, Limit: limit,
		})
		if test.Duration > limit {
			breached = append(breached, id)
		}
	}
	if err := s.ciHealth().SaveTests(ctx, rows); err != nil {
		log.Printf("PR #%d run %s: saving CI health tests: %v", r.Number, runToken(run), err)
		return nil
	}
	return breached
}

// recordableTests keeps the tests worth watching: all over the limit, then
// the slowest under it. A suite that ran isolated re-runs counts a breach
// only when a run alone confirmed it: a loaded runner inflates a full-run
// time, and the isolated list is cut at a fixed count and a 30-second budget,
// so an unconfirmed time over the limit is the load, not the test. A suite
// without isolation (the relay one) has no confirmation to wait for, so its
// over-limit times count as they always did.
func recordableTests(tests []cibudget.Test, limit time.Duration) []cibudget.Test {
	sorted := append([]cibudget.Test(nil), tests...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Duration > sorted[j].Duration })
	seen := map[string]bool{}
	var over, under []cibudget.Test
	for _, test := range sorted {
		id := test.ID()
		if seen[id] {
			continue
		}
		seen[id] = true
		if test.Duration > limit {
			if test.IsolationRan && !test.Isolated {
				continue
			}
			over = append(over, test)
			continue
		}
		under = append(under, test)
	}
	if len(over) > maxStoredTests {
		over = over[:maxStoredTests]
	}
	if len(under) > slowestUnder {
		under = under[:slowestUnder]
	}
	return append(over, under...)
}

// saveCIRun records one run, its jobs and their steps from the job list the
// budget already read, so the view never downloads a report of its own.
func (s *Service) saveCIRun(ctx context.Context, r Record, run Run, warn, fail, busy, queue time.Duration, jobs []cihealth.Job) {
	started, err := time.Parse(time.RFC3339, run.Started)
	if err != nil {
		started = time.Time{}
	}
	row := cihealth.Run{
		Room: r.RoomID, Token: runToken(run), Repo: r.Repo, PR: r.Number, Head: run.HeadSHA,
		Workflow: Sanitize(run.Name), Started: started, Status: run.Status, Conclusion: run.Conclusion,
		Queue: queue, Busy: busy, Warn: warn, Fail: fail,
	}
	if err := s.ciHealth().SaveRun(ctx, row, jobs); err != nil {
		log.Printf("PR #%d run %s: saving CI health run: %v", r.Number, runToken(run), err)
	}
}

// postSlowTestCrossings posts one quiet line for each test that newly broke
// the limit in a separate run. The post names the test, its package and the
// feature that owns it, so the room can route it without waking anyone: the
// line is stored in the feed and starts no session.
func (s *Service) postSlowTestCrossings(ctx context.Context, r Record, run Run, tests []string) {
	if len(tests) == 0 {
		return
	}
	rec, err := s.roomsStore().Resolve(r.RoomID)
	if err != nil {
		log.Printf("PR #%d: CI health cannot resolve room: %v", r.Number, err)
		return
	}
	token := runToken(run)
	crossings, err := s.ciHealth().Crossings(ctx, r.RoomID, token, tests, slowTestRepeatGap)
	if err != nil {
		log.Printf("PR #%d run %s: reading CI health crossings: %v", r.Number, token, err)
		return
	}
	if len(crossings) == 0 {
		return
	}
	entries := s.libraryEntries(ctx, rec.Repo)
	ev := room.SlowTestEvent{Run: runLabel(token), Count: len(crossings)}
	lines := make([]string, 0, len(crossings))
	reported := make([]string, 0, len(crossings))
	for _, crossing := range crossings {
		features := library.PackageOwners(entries, crossing.Now.Package)
		lines = append(lines, slowTestLine(crossing, features))
		reported = append(reported, crossing.Test)
		ev.Crossings = append(ev.Crossings, room.SlowTestCrossing{
			Test: crossing.Test, Package: crossing.Now.Package, Features: features,
			DurationMS: int(crossing.Now.Duration / time.Millisecond), Run: runLabel(crossing.Now.Token),
			OtherMS: int(crossing.Other.Duration / time.Millisecond), OtherRun: runLabel(crossing.Other.Token),
			LimitMS: int(crossing.Now.Limit / time.Millisecond),
		})
	}
	ev.Detail = strings.Join(lines, "\n")
	// The line is quiet: it reaches the owner feed as a card and starts no
	// session.
	if _, err := s.roomsStore().PostEventInput(room.PostEventInput{
		RoomID: rec.ID, Event: ev, To: []string{rec.Name}, RoomService: true,
	}); err != nil {
		log.Printf("PR #%d run %s: posting CI health line: %v", r.Number, token, err)
		return
	}
	if err := s.ciHealth().MarkNotified(ctx, r.RoomID, reported, token); err != nil {
		log.Printf("PR #%d run %s: marking CI health line: %v", r.Number, token, err)
	}
}

// roomsStore is the room store the engine wire supplies, or the default one
// in a process that has no engine, such as a test.
func (s *Service) roomsStore() *room.Store {
	if s.Notify != nil && s.Notify.Rooms != nil {
		return s.Notify.Rooms
	}
	return room.DefaultStore()
}

// slowTestLine is the one feed line for a crossing: what the test is, who
// owns the code, both times and the limit. No wall of text, no second post.
func slowTestLine(crossing cihealth.Crossing, features []string) string {
	var b strings.Builder
	b.WriteString(crossing.Test)
	if crossing.Now.Package != "" {
		b.WriteString(" · " + crossing.Now.Package)
	}
	if len(features) > 0 {
		b.WriteString(" · " + strings.Join(features, ", "))
	}
	fmt.Fprintf(&b, " · %s at %s, %s at %s · limit %s",
		seconds(crossing.Now.Duration), runLabel(crossing.Now.Token),
		seconds(crossing.Other.Duration), runLabel(crossing.Other.Token),
		seconds(crossing.Now.Limit))
	return b.String()
}

// runLabel names a run token (id/attempt) as a person reads it: #1241.
func runLabel(token string) string {
	id, _, _ := strings.Cut(token, "/")
	if id == "" {
		return token
	}
	return "#" + id
}

// libraryEntries reads the room's feature map once per tick that posts. A
// missing map or a room without a checkout is ordinary: the line then names
// the package only.
func (s *Service) libraryEntries(ctx context.Context, repoDir string) []library.Entry {
	if repoDir == "" {
		return nil
	}
	id, err := repoid.Resolve(ctx, repoDir)
	if err != nil {
		return nil
	}
	entries, err := library.Store{DataDir: s.ciHealth().DataDir}.List(id.ID, false)
	if err != nil {
		return nil
	}
	return entries
}

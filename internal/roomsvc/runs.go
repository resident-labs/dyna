package roomsvc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/textutil"
	"dyna-agent/internal/workeroutput"
)

// Bounds on what one run read returns. A run can hold a thousand agent
// calls and megabytes of logs; a view needs the recent part.
const (
	MaxRunsListed = 50
	MaxRunAgents  = 200
	MaxRunLogs    = 100
	MaxRunPhases  = 50
	MaxRunPreview = 300
	MaxRunJournal = 200
)

// RunSummary is one workflow run in a room's list.
type RunSummary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // running|ok|error|canceled
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
	RoomID    string    `json:"roomId"`
	// Parent is the name of the session that started the run; empty when a
	// shell or the owner started it.
	Parent string `json:"parent,omitempty"`
	// ParentID is that session's ID, for the run view's breadcrumb.
	ParentID string `json:"parentId,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	// Paused is set on a running run that starts no new agents.
	Paused bool   `json:"paused,omitempty"`
	Error  string `json:"error,omitempty"`
	// Agents, Done, Failed and Working count the run's agent calls, for a
	// progress bar.
	Agents  int `json:"agents"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
	Working int `json:"working"`
}

// RunList is a room's newest runs plus counts over all of them.
type RunList struct {
	Runs    []RunSummary `json:"runs"`
	Total   int          `json:"total"`
	Running int          `json:"running"`
}

// RunAgent is one agent call in a run, folded from its events.
type RunAgent struct {
	ID      int    `json:"id"`
	Label   string `json:"label"`
	Profile string `json:"profile,omitempty"`
	Phase   string `json:"phase,omitempty"`
	// Lane and Stage are what the script gave its phase; both are empty when it gave none.
	Lane  string `json:"lane,omitempty"`
	Stage string `json:"stage,omitempty"`
	// Status is running, ok, error, or the end status the engine wrote.
	// "unknown" means the run ended without an end event for this agent.
	Status  string `json:"status"`
	Started int64  `json:"started,omitempty"` // unix millis; 0 when the start event was not read
	DurMs   int64  `json:"durMs,omitempty"`
	Preview string `json:"preview,omitempty"`
	// Doing says what a running worker does now (see
	// workeroutput.LastActivity); empty once it ended.
	Doing workeroutput.Activity `json:"doing,omitzero"`
	// Session is the ID of the session that ran this agent. It is empty
	// for an agent of a run from before agents were sessions.
	Session string `json:"session,omitempty"`
	Error   string `json:"error,omitempty"`
	Cached  bool   `json:"cached,omitempty"`
}

// RunLog is one workflow log line.
type RunLog struct {
	TS  int64  `json:"ts"`
	Msg string `json:"msg"`
}

// RunDetail is one run's read-only view.
type RunDetail struct {
	Run    RunSummary `json:"run"`
	Agents []RunAgent `json:"agents"`
	// AgentTotal, AgentRunning and AgentFailed count every agent call;
	// Agents holds the last MaxRunAgents.
	AgentTotal   int      `json:"agentTotal"`
	AgentRunning int      `json:"agentRunning"`
	AgentFailed  int      `json:"agentFailed"`
	Phases       []string `json:"phases,omitempty"`
	Logs         []RunLog `json:"logs"`
	// ResultSummary is one line from the run result and ResultBytes its
	// size; RunResult loads the result itself.
	ResultSummary string `json:"resultSummary,omitempty"`
	ResultBytes   int    `json:"resultBytes,omitempty"`
}

// RunResultView is a run's or one worker's full result, as JSON text.
// TruncatedKB is set when the result passed the content file limit; Result
// then holds its start.
type RunResultView struct {
	Result      string `json:"result"`
	Bytes       int    `json:"bytes"`
	TruncatedKB int    `json:"truncatedKB,omitempty"`
	// Journal is a worker's dyna journal, newest first, at most
	// MaxRunJournal entries.
	Journal []RunJournalEntry `json:"journal,omitempty"`
}

// RunJournalEntry is one line of a worker's journal.
type RunJournalEntry struct {
	TS      int64  `json:"ts"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Next    string `json:"next,omitempty"`
}

// runIndex caches run metas by the runs table's revision, so a scan over a
// store with thousands of runs reads only the rows written since the last.
type runIndex struct {
	mu      sync.Mutex
	rev     int64
	entries map[string]runEntry
	counts  map[string]agentCounts
}

// agentCounts caches a run's agent tallies by its events file size, which
// only grows while the run appends, and by whether the run was live. A run
// that ends stops growing, but its unfinished agents turn from working to
// unknown, so the end must recount.
type agentCounts struct {
	size                         int64
	live                         bool
	total, done, failed, working int
}

type runEntry struct {
	rev  int64 // the index revision that stored it, which changes with its row
	meta runstore.Meta
	room string // resolved room ID
}

// runs returns every live run in the store, newest first, with its room.
func (s *Service) runs() []runEntry {
	s.runIdx.mu.Lock()
	defer s.runIdx.mu.Unlock()
	c, err := runstore.ChangesSince(s.runIdx.rev)
	if err != nil {
		return nil
	}
	if s.runIdx.entries == nil {
		s.runIdx.entries = map[string]runEntry{}
	}
	for id, m := range c.Changed {
		s.runIdx.entries[id] = runEntry{rev: c.Rev, meta: m, room: s.runRoom(m)}
	}
	s.runIdx.rev = c.Rev
	live := make(map[string]bool, len(c.Live))
	var out []runEntry
	for _, id := range c.Live {
		live[id] = true
		ent, ok := s.runIdx.entries[id]
		if !ok {
			// A run back from the archive keeps its row's revision, and a
			// starting run has no row yet.
			m, err := runstore.ReadMeta(id)
			if err != nil {
				continue
			}
			ent = runEntry{rev: c.Rev, meta: m, room: s.runRoom(m)}
			s.runIdx.entries[id] = ent
		}
		out = append(out, ent)
	}
	for id := range s.runIdx.entries {
		if !live[id] {
			delete(s.runIdx.entries, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].meta.StartedAt.After(out[j].meta.StartedAt) })
	return out
}

// runRoom places a run: its stamped room, else the room of the session that
// started it (older metas lack RoomID). Other untagged runs have no room.
// TODO: legacy/shim remove later — places old runs without roomId using their parent session's room; remove after retained run metadata has room stamps
func (s *Service) runRoom(m runstore.Meta) string {
	if m.RoomID != "" {
		return m.RoomID
	}
	if m.ParentSession != "" {
		if info, err := s.sessions().Inspect(m.ParentSession); err == nil && info.RoomID != "" {
			return info.RoomID
		}
	}
	return ""
}

func (s *Service) summary(ent runEntry) RunSummary {
	m := ent.meta
	r := RunSummary{ID: m.ID, Name: m.Name, Status: m.Status, StartedAt: m.StartedAt, EndedAt: m.EndedAt, RoomID: ent.room, Detached: m.Detached, Paused: runstore.IsPaused(m.ID), Error: m.Error}
	c := s.counts(m.ID, m.Status == "running")
	r.Agents, r.Done, r.Failed, r.Working = c.total, c.done, c.failed, c.working
	if m.ParentSession != "" {
		if info, err := s.sessions().Inspect(m.ParentSession); err == nil {
			r.Parent, r.ParentID = info.EffectiveName(), info.ID
		}
	}
	return r
}

// Runs lists a room's workflow runs: running runs first, then newest
// first, at most MaxRunsListed.
func (s *Service) Runs(roomRef string) (RunList, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return RunList{}, err
	}
	out := RunList{Runs: []RunSummary{}}
	var running, rest []runEntry
	for _, ent := range s.runs() {
		if ent.room != rec.ID {
			continue
		}
		out.Total++
		if ent.meta.Status == "running" {
			out.Running++
			running = append(running, ent)
		} else {
			rest = append(rest, ent)
		}
	}
	for _, ent := range append(running, rest...) {
		if len(out.Runs) == MaxRunsListed {
			break
		}
		out.Runs = append(out.Runs, s.summary(ent))
	}
	return out, nil
}

// RunDetail reads one run: its meta, a flat agent list folded from
// events.jsonl, its workflow logs and its result, each bounded.
func (s *Service) RunDetail(id string) (RunDetail, error) {
	if err := runstore.ValidateRunID(id); err != nil {
		return RunDetail{}, err
	}
	m, err := runstore.ReadMeta(id)
	if err != nil {
		return RunDetail{}, err
	}
	out := RunDetail{Run: s.summary(runEntry{meta: m, room: s.runRoom(m)}), Agents: []RunAgent{}, Logs: []RunLog{}}
	// An unreadable events file degrades to an empty agent list; the meta
	// and the result still show.
	events, _, _ := runstore.ReadEventsFrom(id, 0)
	out.Agents, out.AgentTotal, out.Phases, out.Logs = foldRun(events, m.Status == "running")
	out.AgentRunning, out.AgentFailed = countAgents(events, m.Status == "running")
	for i, a := range out.Agents {
		session := runstore.AgentSessionID(id, a.ID)
		if _, err := os.Lstat(filepath.Join(runstore.SessionsDir(), session)); err == nil {
			out.Agents[i].Session = session
		}
		if a.Status == "running" {
			if path, err := runstore.AgentNormalizedOutputPath(id, a.ID); err == nil {
				out.Agents[i].Doing = workeroutput.LastActivity(path)
			}
		}
	}
	if res, ok := runstore.ReadResult(id); ok {
		out.ResultSummary, out.ResultBytes = runstore.Summary(res), len(res)
	}
	return out, nil
}

// RunResult loads a run's result (agent 0) or one worker's result from the
// run journal, in full up to the content file limit.
func (s *Service) RunResult(id string, agent int) (out RunResultView, err error) {
	if err := runstore.ValidateRunID(id); err != nil {
		return RunResultView{}, err
	}
	var res string
	if agent == 0 {
		r, ok := runstore.ReadResult(id)
		if !ok {
			return RunResultView{}, fmt.Errorf("run %s has no result", id)
		}
		res = r
	} else {
		r, found, err := runstore.JournalResult(id, agent)
		if err != nil && !os.IsNotExist(err) {
			return RunResultView{}, err
		}
		res = r
		journal := workerJournal(id, agent)
		// A running worker has a journal before it has a result.
		if !found && len(journal) == 0 {
			return RunResultView{}, fmt.Errorf("worker %d of run %s has no result", agent, id)
		}
		defer func() { out.Journal = journal }()
	}
	out = RunResultView{Result: res, Bytes: len(res)}
	limits, err := settings.Content()
	if err != nil {
		return RunResultView{}, err
	}
	if max := int(limits.FileBytes); len(res) > max {
		out.Result, out.TruncatedKB = strings.ToValidUTF8(res[:max], ""), max/1024
	}
	return out, nil
}

// foldRun turns run events into agents, phases and logs. An agent whose
// start event is missing keeps an empty start, not an error; one with no
// end event is running while the run runs, else unknown.
func foldRun(events []runstore.Event, live bool) (agents []RunAgent, total int, phases []string, logs []RunLog) {
	agents, total, phases, logs = foldAll(events, live)
	if len(agents) > MaxRunAgents {
		agents = agents[len(agents)-MaxRunAgents:]
	}
	if len(phases) > MaxRunPhases {
		phases = phases[len(phases)-MaxRunPhases:]
	}
	if len(logs) > MaxRunLogs {
		logs = logs[len(logs)-MaxRunLogs:]
	}
	return agents, total, phases, logs
}

// foldAll folds every event with no caps.
func foldAll(events []runstore.Event, live bool) (agents []RunAgent, total int, phases []string, logs []RunLog) {
	byID := map[int]int{}
	get := func(e runstore.Event) *RunAgent {
		i, ok := byID[e.ID]
		if !ok {
			i = len(agents)
			byID[e.ID] = i
			agents = append(agents, RunAgent{ID: e.ID, Status: "running"})
		}
		return &agents[i]
	}
	for _, e := range events {
		switch e.T {
		case "phase":
			if e.Title != "" {
				phases = append(phases, e.Title)
			}
		case "agent_start":
			a := get(e)
			a.Label, a.Profile, a.Phase, a.Started = e.Label, e.Profile, e.Phase, e.TS
			a.Lane, a.Stage = e.Lane, e.Stage
		case "agent_end":
			a := get(e)
			if a.Label == "" {
				a.Label = e.Label
			}
			a.Status = e.Status
			if a.Status == "" {
				a.Status = "ok"
			}
			a.DurMs, a.Cached = e.DurMs, e.Cached
			a.Preview, a.Error = textutil.Clip(e.Preview, MaxRunPreview), textutil.Clip(e.Error, MaxRunPreview)
		case "log":
			if e.Provenance != "system" && e.Msg != "" {
				logs = append(logs, RunLog{TS: e.TS, Msg: textutil.Clip(e.Msg, MaxRunPreview)})
			}
		}
	}
	for i := range agents {
		if agents[i].Status == "running" && !live {
			agents[i].Status = "unknown"
		}
	}
	total = len(agents)
	if agents == nil {
		agents = []RunAgent{}
	}
	if logs == nil {
		logs = []RunLog{}
	}
	return agents, total, phases, logs
}

// runSigs returns one signature per room over its runs' ids, statuses and
// revisions, for Watch.
func (s *Service) runSigs() map[string]string {
	out := map[string]string{}
	for _, ent := range s.runs() {
		out[ent.room] += ent.meta.ID + ent.meta.Status + strconv.FormatInt(ent.rev, 10) + ";"
	}
	return out
}

// countAgents counts running and failed agents over the whole run, so the
// header stays true when the list shows only the last MaxRunAgents.
func countAgents(events []runstore.Event, live bool) (running, failed int) {
	all, _, _, _ := foldAll(events, live)
	for _, a := range all {
		switch a.Status {
		case "running":
			running++
		case "error", "failed":
			failed++
		}
	}
	return running, failed
}

// counts tallies a run's agents, re-reading events.jsonl only when it grew.
func (s *Service) counts(id string, live bool) agentCounts {
	fi, err := os.Stat(filepath.Join(runstore.RunDir(id), "events.jsonl"))
	if err != nil {
		return agentCounts{}
	}
	s.runIdx.mu.Lock()
	c, ok := s.runIdx.counts[id]
	s.runIdx.mu.Unlock()
	if ok && c.size == fi.Size() && c.live == live {
		return c
	}
	events, _, _ := runstore.ReadEventsFrom(id, 0)
	all, _, _, _ := foldAll(events, live)
	c = agentCounts{size: fi.Size(), live: live, total: len(all)}
	for _, a := range all {
		switch a.Status {
		case "ok":
			c.done++
		case "running":
			c.working++
		case "error", "failed":
			c.failed++
		default:
			c.done++
		}
	}
	s.runIdx.mu.Lock()
	if s.runIdx.counts == nil {
		s.runIdx.counts = map[string]agentCounts{}
	}
	s.runIdx.counts[id] = c
	s.runIdx.mu.Unlock()
	return c
}

// workerJournal reads a worker's journal, newest first, keeping the last
// MaxRunJournal entries. A missing or unreadable journal gives none.
func workerJournal(id string, agent int) []RunJournalEntry {
	entries, _, err := runstore.ReadAgentJournalFrom(id, agent, 0)
	if err != nil {
		return nil
	}
	if len(entries) > MaxRunJournal {
		entries = entries[len(entries)-MaxRunJournal:]
	}
	out := make([]RunJournalEntry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		out = append(out, RunJournalEntry{TS: e.TS, Kind: e.Kind, Message: e.Message, Next: e.Next})
	}
	return out
}

package roomsvc

import (
	"dyna-agent/internal/workeroutput"
	"encoding/json"
	"time"
)

type RunSource struct {
	Script string `json:"script,omitempty"`
}
type StartedRun struct {
	ID string `json:"id"`
}

type StartNamedRunRequest struct {
	Room     string          `json:"room"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	ResultTo string          `json:"resultTo,omitempty"`
}

// RunSummary is one workflow run in a room's list.
type RunSummary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // running|ok|error|canceled
	StartedAt time.Time `json:"startedAt"`
	// EndedAt is always encoded: Go does not omit a zero time.Time with omitempty.
	EndedAt time.Time `json:"endedAt"`
	RoomID  string    `json:"roomId"`
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

// RunCommand is one run() command call in a run, folded from its events.
// Its ID counts commands, not agents, so a run can hold both an agent 1 and
// a command 1.
type RunCommand struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
	Phase string `json:"phase,omitempty"`
	// Lane and Stage are what the script gave its phase; both are empty when it gave none.
	Lane  string `json:"lane,omitempty"`
	Stage string `json:"stage,omitempty"`
	// Status is running, ok, error, or unknown when the run ended without
	// an end event for this command.
	Status  string `json:"status"`
	Started int64  `json:"started,omitempty"` // unix millis; 0 when the start event was not read
	// Command is the command line the script asked run() to execute.
	Command string `json:"command,omitempty"`
	// Shell is the managed shell that ran it.
	Shell    string `json:"shell,omitempty"`
	DurMs    int64  `json:"durMs,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"` // nil until the command ended
	Cached   bool   `json:"cached,omitempty"`   // satisfied from a resumed run's journal
	Preview  string `json:"preview,omitempty"`
	// Tail is the last lines of the command's output.
	Tail  string `json:"tail,omitempty"`
	Error string `json:"error,omitempty"`
}

// RunLog is one workflow log line.
type RunLog struct {
	TS  int64  `json:"ts"`
	Msg string `json:"msg"`
}

// RunDetail is one run's read-only view.
type RunDetail struct {
	Run      RunSummary   `json:"run"`
	Agents   []RunAgent   `json:"agents"`
	Commands []RunCommand `json:"commands"`
	// AgentTotal, AgentRunning and AgentFailed count every agent call;
	// Agents holds the last MaxRunAgents.
	AgentTotal   int `json:"agentTotal"`
	AgentRunning int `json:"agentRunning"`
	AgentFailed  int `json:"agentFailed"`
	// CommandTotal, CommandRunning and CommandFailed count every run()
	// command the same way; Commands holds the last MaxRunCommands.
	CommandTotal   int      `json:"commandTotal"`
	CommandRunning int      `json:"commandRunning"`
	CommandFailed  int      `json:"commandFailed"`
	Phases         []string `json:"phases,omitempty"`
	Logs           []RunLog `json:"logs"`
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

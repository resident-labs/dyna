package hooks

import "time"

type Hook struct {
	Name    string `json:"name"`
	Label   string `json:"label,omitempty"`
	Event   string `json:"event"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	Timeout string `json:"timeout"`
	OnFail  string `json:"onFail"`
	// Scope says where the hook belongs: one repository, or the machine. A
	// hook stored before scope existed belongs to its repository.
	Scope Scope `json:"scope" tstype:"'repository' | 'global' | ''"`
	// Builtin names the table entry a hook the engine seeded runs, and is empty
	// for a hook the owner added. The engine fixes its command and cannot
	// remove it.
	Builtin      Builtin    `json:"builtin,omitempty" tstype:"string"`
	AddedBy      string     `json:"addedBy"`
	AddedAt      time.Time  `json:"addedAt"`
	EditedBy     string     `json:"editedBy,omitempty"`
	EditedAt     *time.Time `json:"editedAt,omitempty"`
	EveryMinutes int        `json:"everyMinutes,omitempty"`
	// Disabled keeps the hook's settings and stops automatic runs. It is
	// absent in older hook documents, where the hook runs.
	Disabled bool `json:"disabled,omitempty"`
}

// RunResult is the outcome of one hook run. Every producer and every reader of
// a run uses these constants, so a renamed value fails to compile instead of
// misclassifying a run.
type RunResult string

const (
	// ResultPending is a run that has started and not yet ended.
	ResultPending RunResult = "pending"
	// ResultPass is a run whose hook exited cleanly.
	ResultPass RunResult = "pass"
	// ResultError is a run that failed to start, timed out or lost its runner.
	ResultError RunResult = "error"
	// ResultFail is a run whose hook exited with a failure status.
	ResultFail RunResult = "fail"
	// ResultInstalling is a run that found the install unit for its commit
	// already active. That unit records the result, so this run is not an error.
	ResultInstalling RunResult = "installing"
)

// Run records one repository hook attempt independently of test evidence.
type Run struct {
	RunnerSession string    `json:"runnerSession,omitempty"`
	ID            string    `json:"id"`
	Repo          string    `json:"repo"`
	Name          string    `json:"name"`
	Command       string    `json:"command"`
	Result        RunResult `json:"result"`
	LogPath       string    `json:"logPath"`
	StartedAt     time.Time `json:"startedAt"`
	FinishedAt    time.Time `json:"finishedAt,omitempty"`
	// Event is the event that started the run, and Main is the repository's
	// main checkout at that moment. A run that an engine leaves pending has no
	// hook left to ask, so it keeps what its result card needs: the event, the
	// label, the checkout to post to and the room the event belongs to. Room is
	// empty when the repository's own room is the place to post.
	Event string `json:"event,omitempty"`
	Label string `json:"label,omitempty"`
	Main  string `json:"main,omitempty"`
	Room  string `json:"room,omitempty"`
}

// Summary is one hook with the newest run recorded for it, so a page can show
// each hook's state without reading every run. LastRun is nil when the hook
// has never run.
type Summary struct {
	Hook    Hook `json:"hook"`
	LastRun *Run `json:"lastRun,omitempty"`
	// NextRun is when the schedule next runs this hook. It is nil for a hook
	// that does not run on a schedule.
	NextRun *time.Time `json:"nextRun,omitempty"`
}

// HookRunLog is the end of one stored hook run's log, with its exit code
// when the log records one (-1 if not).
type HookRunLog struct {
	Tail     string `json:"tail"`
	ExitCode int    `json:"exitCode"`
}

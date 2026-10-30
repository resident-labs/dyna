package serviceregistry

import "time"

// ID names one service. The set is closed.
type ID string

// Group is the section of the Services page that holds a service.
type Group string

// State is what a service is doing now. It is data: the page shows a colored
// chip only for the states that need attention.
type State string

// Detail is the short reason behind a state. The page words it; it is never
// raw error text.
type Detail string

// The services of this release.
const (
	IDReviews         ID = "reviewer"
	IDMergeQueue      ID = "merge-queue"
	IDLocalCI         ID = "local-ci"
	IDPRWatcher       ID = "pr-watcher"
	IDLaneSync        ID = "lane-sync"
	IDIssueWatch      ID = "issue-watch"
	IDLibrary         ID = "library"
	IDParentCheckIns  ID = "parent-checkins"
	IDVoice           ID = "voice"
	IDUpdates         ID = "updates"
	IDLoadDoctor      ID = "doctor"
	IDMaintenance     ID = "maintenance"
	IDAccountUsage    ID = "account-usage"
	IDHarnessVersions ID = "harness-versions"
)

const (
	GroupPullRequests Group = "pull-requests"
	GroupAgents       Group = "agents"
	GroupThisComputer Group = "this-computer"
)

const (
	// StateOn means the service is enabled and healthy.
	StateOn State = "on"
	// StateRunning means it is working now.
	StateRunning State = "running"
	// StateWaiting means it has work but cannot proceed yet.
	StateWaiting State = "waiting"
	// StateIdle means it is enabled and has nothing to do now.
	StateIdle State = "idle"
	// StateOff means the owner turned it off, or it is unavailable.
	StateOff State = "off"
	// StateError means its newest run failed.
	StateError State = "error"
	// StateNotSetUp means nothing is configured for it.
	StateNotSetUp State = "not-set-up"
)

const (
	DetailNone Detail = ""
	// DetailLastRunFailed marks a service whose newest run failed.
	DetailLastRunFailed Detail = "last-run-failed"
	// DetailNoRunYet marks a scheduled service that has not run yet.
	DetailNoRunYet Detail = "no-run-yet"
	// DetailProfileWaiting marks an agent service whose profile cannot run.
	DetailProfileWaiting Detail = "profile-waiting"
	// DetailWorking marks an agent service busy with a task; Count is its backlog.
	DetailWorking Detail = "working"
	// DetailQueued marks a service with Count items waiting.
	DetailQueued Detail = "queued"
	// DetailPaused marks a service the owner paused.
	DetailPaused Detail = "paused"
	// DetailInCall marks voice during a call.
	DetailInCall Detail = "in-a-call"
	// DetailUnavailable marks a service that cannot run on this machine.
	DetailUnavailable Detail = "unavailable"
	// DetailUnreadable marks a service whose facts could not be read.
	DetailUnreadable Detail = "unreadable"
)

// Switch is a service's on/off control and its current value.
type Switch struct {
	On bool `json:"on"`
}

// Schedule is how often a built-in service runs and how long one run may
// take. The owner can change both and reset them to the defaults.
type Schedule struct {
	EveryMinutes int `json:"everyMinutes"`
	// Timeout is a Go duration such as "5m0s".
	Timeout string `json:"timeout"`
}

// Service is one row of the registry.
type Service struct {
	ID          ID     `json:"id"`
	Group       Group  `json:"group"`
	Label       string `json:"label"`
	Description string `json:"description"`
	State       State  `json:"state"`
	Detail      Detail `json:"detail"`
	// Count is the number the detail talks about, such as items waiting.
	Count   int        `json:"count"`
	LastRun *time.Time `json:"lastRun"`
	NextRun *time.Time `json:"nextRun"`
	// Switch is nil for a service the owner cannot turn off.
	Switch *Switch `json:"switch"`
	// Schedule is set for a built-in service whose timing the owner can edit.
	Schedule *Schedule `json:"schedule"`
	// Profile is the agent profile the service runs on; empty for a service
	// that runs no agent.
	Profile string `json:"profile"`
	// Session is the address of the agent session the service runs in, or empty.
	Session string `json:"session"`
	// Hook names the repository hook that does this service's work, such as
	// self-update for Updates, or is empty. The Services page shows that hook
	// under the service and leaves it out of the hook list.
	Hook         string   `json:"hook"`
	SettingsKeys []string `json:"settingsKeys"`
}

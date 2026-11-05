package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type InstructionsGetRequest struct {
	Room RoomID `json:"room,omitempty"`
}
type InstructionsGetResponse struct {
	Text    string     `json:"text"`
	Hash    string     `json:"hash"`
	SavedAt *time.Time `json:"savedAt,omitempty"`
}
type InstructionsSetRequest struct {
	Room RoomID `json:"room,omitempty"`
	Text string `json:"text"`
	Base string `json:"base"`
}
type InstructionsSetResponse struct {
	Hash string `json:"hash"`
}

type Method string

const (
	MethodInstructionsGet         Method = "instructions.get"
	MethodInstructionsSet         Method = "instructions.set"
	MethodWebLogin                Method = "web.login"
	MethodWebSearch               Method = "web.search"
	MethodWebsearchSetKey         Method = "websearch.set_key"
	MethodWebsearchRemoveKey      Method = "websearch.remove_key"
	MethodProxyKey                Method = "proxy.key"
	MethodProxyUsage              Method = "proxy.usage"
	MethodConversationRead        Method = "conversation.read"
	MethodRoomList                Method = "room.list"
	MethodSessionList             Method = "session.list"
	MethodDiffSaved               Method = "diff.saved"
	MethodDeliveryDone            Method = "delivery.done"
	MethodHostAdd                 Method = "host.add"
	MethodHostSetLimits           Method = "host.set_limits"
	MethodHostSet                 Method = "host.set"
	MethodHostList                Method = "host.list"
	MethodAccountsList            Method = "accounts.list"
	MethodAccountsConnect         Method = "accounts.connect"
	MethodAccountsReconnect       Method = "accounts.reconnect"
	MethodAccountsConnectCallback Method = "accounts.connect_callback"
	MethodAccountsConnectStatus   Method = "accounts.connect_status"
	MethodAccountsSetKey          Method = "accounts.set_key"
	MethodAccountsImportOAuth     Method = "accounts.import_oauth"
	MethodAccountsRemove          Method = "accounts.remove"
	MethodAccountsSetLimits       Method = "accounts.set_limits"
	MethodAccountsRename          Method = "accounts.rename"
	MethodProfileSet              Method = "profile.set"
	MethodProfileRemove           Method = "profile.remove"
	MethodProfileList             Method = "profile.list"
	MethodAccountsQuota           Method = "accounts.quota"
	MethodRoomPause               Method = "room.pause"
	MethodRoomSetLimits           Method = "room.set_limits"
	MethodRoomSet                 Method = "room.set"
	MethodRoomCreate              Method = "room.create"
	MethodRoomDelete              Method = "room.delete"
	MethodRoomRepoAdd             Method = "room.repo.add"
	MethodRoomRepos               Method = "room.repos"
	MethodRepoCISet               Method = "repo.ci.set"
	MethodSessionStart            Method = "session.start"
	MethodComposeList             Method = "compose.list"
	MethodSessionClose            Method = "session.close"
	MethodSessionStop             Method = "session.stop"
	MethodSessionSetProfile       Method = "session.set_profile"
	MethodSessionAccount          Method = "session.account"
	MethodSessionSetAccount       Method = "session.set_account"
	MethodSessionSetBranch        Method = "session.set_branch"
	MethodSessionRestart          Method = "session.restart"
	MethodSessionRefresh          Method = "session.refresh"
	MethodSessionMove             Method = "session.move"
	MethodPost                    Method = "post"
	MethodPostSearch              Method = "post.search"
	MethodSessionSearch           Method = "session.search"
	MethodEventsList              Method = "events.list"
	MethodBoxCompacted            Method = "box.compacted"
	MethodBoxHello                Method = "box.hello"
	MethodComputerChanged         Method = "computer.changed"
	MethodComputerStart           Method = "computer.start"
	MethodTurnOutput              Method = "turn.output"
	MethodTurnActivity            Method = "turn.activity"
	MethodTranscriptFetch         Method = "transcript.fetch"
	MethodTranscriptAppend        Method = "transcript.append"
	MethodTranscriptRead          Method = "transcript.read"
	MethodSessionWork             Method = "session.work"
	MethodShellJobEnded           Method = "shell.job.ended"
	MethodShellJobRunning         Method = "shell.job.running"
	MethodShellOutput             Method = "shell.output"
)

type ErrorCode string

const (
	ErrPodmanRequired       ErrorCode = "podman_required"
	ErrInvalid              ErrorCode = "invalid"
	ErrUnknownAction        ErrorCode = "unknown_action"
	ErrNotFound             ErrorCode = "not_found"
	ErrForbidden            ErrorCode = "forbidden"
	ErrCrossRoomAttachment  ErrorCode = "cross_room_attachment"
	ErrConflict             ErrorCode = "conflict"
	ErrRoomLimited          ErrorCode = "room_limited"
	ErrProfileExists        ErrorCode = "profile_exists"
	ErrMoveBusy             ErrorCode = "move_busy"
	ErrHasChildren          ErrorCode = "has_children"
	ErrParentClosing        ErrorCode = "parent_closing"
	ErrSessionClosing       ErrorCode = "session_closing"
	ErrStale                ErrorCode = "stale"
	ErrGitHubQuota          ErrorCode = "github_quota" // the message names the reset time
	ErrVisualSuperseded     ErrorCode = "visual_superseded"
	ErrUnavailable          ErrorCode = "unavailable"
	ErrReconnectUnavailable ErrorCode = "reconnect_unavailable"
	ErrModelsUnavailable    ErrorCode = "models_unavailable"
	ErrStoreFromV1          ErrorCode = "store_from_v1"
	ErrStoreTooOld          ErrorCode = "store_too_old"
	ErrNoGPU                ErrorCode = "no_gpu"        // a session start asked for a GPU and no host reports one
	ErrGPUTooLarge          ErrorCode = "gpu_too_large" // the request exceeds what the largest card can give a box
)

type Error struct {
	Code         ErrorCode            `json:"code"`
	Message      string               `json:"message"`
	Visual       *VisualSuperseded    `json:"visual,omitempty"`
	Limited      *RoomLimited         `json:"limited,omitempty"`
	Workflow     *WorkflowRefused     `json:"workflow,omitempty"`
	GitHubWrites *GitHubWritesRefused `json:"githubWrites,omitempty"`
}

// errorBytes bounds what ReadError reads of a refusal.
const errorBytes = 64 << 10

// ReadError reads the answer to a refused call: the engine's JSON error, or the plain text of a route that writes one.
func ReadError(body io.Reader) Error {
	data, _ := io.ReadAll(io.LimitReader(body, errorBytes))
	var problem Error
	if err := json.Unmarshal(data, &problem); err != nil {
		return Error{Message: strings.ToValidUTF8(strings.TrimSpace(string(data)), "")}
	}
	return problem
}

// ForeignRoom refuses a session that names a room other than its own.
func ForeignRoom(room, own RoomID) *Error {
	return &Error{Code: ErrForbidden, Message: fmt.Sprintf("room %q is not your room; yours is %s", room, own)}
}

type VisualSuperseded struct {
	Event   EventID `json:"event"`
	Version int     `json:"version"`
	Seq     int64   `json:"seq"`
}

// RoomLimited names the accounts and hosts whose limit holds no other open room. An empty limit means every room,
// so deleting that room would widen them; room.delete refuses until the owner changes those limits.
type RoomLimited struct {
	Accounts []AccountID `json:"accounts,omitempty"`
	Hosts    []HostName  `json:"hosts,omitempty"`
}

type ConversationReadRequest struct {
	Room         RoomID       `json:"room"`
	Conversation Conversation `json:"conversation"`
	Seq          int64        `json:"seq"`
}
type ConversationReadResponse struct{}

type ProfileListRequest struct{}

// ProfileEntry is one profile of profile.list. Profile is the stored profile whole, so it is a valid profile.set body
// as listed; the read-only InUse sits beside it, outside what profile.set takes.
type ProfileEntry struct {
	Profile Profile `json:"profile"`
	// InUse counts the open sessions on the profile.
	InUse int `json:"inUse"`
}
type ProfileListResponse struct {
	Profiles []ProfileEntry `json:"profiles"`
}

type RoomSetLimitsRequest struct {
	Room     RoomID `json:"room"`
	CPUs     int    `json:"cpus"`
	MemBytes int64  `json:"memBytes"`
}
type RoomSetLimitsResponse struct{}

// RoomSetRequest changes a room's settings, each on its own; a nil one stays as it is. Only the owner calls it.
// RootApproves says per kind whether the room's root decides that kind's operations as the owner can; a kind left out stays.
// Every kind is off by default.
type RoomSetRequest struct {
	Room         RoomID                `json:"room"`
	RootApproves map[RootApproval]bool `json:"rootApproves,omitempty"`
	// GitHubWrites sets the room's githubWrites setting whole (spec 7.9): the groups to allow on repositories of the room, or `*`. No groups
	// turns it off. Nil leaves it as it is.
	GitHubWrites *GitHubWrites `json:"githubWrites,omitempty"`
}

// RootApproval names the owner-class operations a room's root may also decide, when the owner allows it for the room.
type RootApproval string

const (
	RootApprovesHostexec RootApproval = "hostexec" // hostexec.grant
	RootApprovesAddons   RootApproval = "addons"   // addon.global
	RootApprovesHosts    RootApproval = "hosts"    // room.hosts
	RootApprovesPools    RootApproval = "pools"    // pool.add
)

// AllRootApprovals lists every kind, in the order the web and the CLI show them.
var AllRootApprovals = []RootApproval{RootApprovesHostexec, RootApprovesAddons, RootApprovesHosts, RootApprovesPools}

type RoomSetResponse struct{}

type RoomPauseRequest struct {
	Room   RoomID `json:"room"`
	Paused bool   `json:"paused"`
}
type RoomPauseResponse struct{}
type SessionStopRequest struct {
	Session SessionID `json:"session"`
}
type SessionStopResponse struct {
	Stopping bool `json:"stopping"`
}

type RoomListRequest struct{}
type RoomSummary struct {
	ID       RoomID    `json:"id"`
	Name     string    `json:"name"`
	Paused   bool      `json:"paused"`
	Created  time.Time `json:"created"`
	LastRead int64     `json:"lastRead"`
	Waiting  []OpID    `json:"waiting"` // pending owner-class operations, which the owner decides, and the root for the kinds RootApproves names
	// RootApproves lists the kinds the room's root may decide.
	RootApproves []RootApproval `json:"rootApproves,omitempty"`
	// GitHubWrites is the room's githubWrites setting, as the owner set it; nil when it is off.
	GitHubWrites *GitHubWrites `json:"githubWrites,omitempty"`
	// Questions are those the owner has not answered.
	Questions []QuestionID `json:"questions,omitempty"`
	Working   []SessionID  `json:"working"`  // open sessions with a running turn
	Failed    int          `json:"failed"`   // open sessions whose last turn that was not stopped failed, or whose box failed or is lost; each counts once
	Hostless  int          `json:"hostless"` // open sessions whose box waits for a room host
	// Deleting marks a room on its way out; it refuses new work. DeleteReason says what holds it, if anything does.
	Deleting     bool   `json:"deleting,omitempty"`
	DeleteReason string `json:"deleteReason,omitempty"`
	// Latest is the newest current-turn activity among the working sessions; none when no working session reported one.
	Latest *RoomActivity `json:"latest,omitempty"`
}
type RoomActivity struct {
	Session SessionID `json:"session"`
	Name    string    `json:"name"`
	Label   string    `json:"label"`
}
type RoomListResponse struct {
	Rooms []RoomSummary `json:"rooms"`
	// Seq is the newest event in any room when the list was read; the owner stream resumes after it.
	Seq int64 `json:"seq"`
}
type SessionListRequest struct {
	Room RoomID `json:"room"`
	// Parent narrows the list to the direct children of one session. Limit caps the sessions returned, and After continues
	// from the session a cut list ended on. A session reading the list without a Limit gets SessionListPage.
	Parent SessionID `json:"parent,omitempty"`
	Limit  int       `json:"limit,omitempty"` // 0 means all, or a page for a session
	After  SessionID `json:"after,omitempty"`
}
type BoxReason string // The stored reason for the current box state, including admission waits and failures.

type SessionSummary struct {
	Start        *BoxStartStatus `json:"start,omitempty"`
	LastRead     int64           `json:"lastRead"`
	ID           SessionID       `json:"id"`
	Name         string          `json:"name"`
	Parent       SessionID       `json:"parent"`
	Profile      ProfileName     `json:"profile"`
	Box          BoxID           `json:"box"`
	BoxState     BoxState        `json:"boxState"`
	BoxReason    BoxReason       `json:"boxReason,omitempty"`
	Host         HostName        `json:"host,omitempty"` // the machine holding the live box; empty before placement and once the box is deleted
	Computer     ComputerState   `json:"computer"`
	Goal         string          `json:"goal"`
	Turn         TurnID          `json:"turn,omitempty"`         // the running turn; a failed or lost box runs none, whatever it left open
	Activity     string          `json:"activity,omitempty"`     // the running turn's latest TurnActivity label
	ActivityKind ActivityKind    `json:"activityKind,omitempty"` // the kind of that activity, set with it
	LastOutcome  TurnOutcome     `json:"lastOutcome,omitempty"`  // of the last ended turn that was not stopped; a stopped turn neither fails nor succeeds
	ShellJobs    int             `json:"shellJobs,omitempty"`    // TurnEnded.ShellJobs of the last ended turn
	Waiting      *SessionWaiting `json:"waiting,omitempty"`      // current wait, only when no turn is running
	HadJobs      bool            `json:"hadJobs"`                // the session has ever run a background shell job, so a finished job stays reachable
	StartedAt    time.Time       `json:"startedAt"`              // when the session was created
	LastTurnAt   time.Time       `json:"lastTurnAt,omitzero"`    // when the latest turn ended; empty when no turn has ended
}
type SessionListResponse struct {
	Sessions []SessionSummary `json:"sessions"`
	// Next is set when the list is cut: the session to pass as After for the rest. Empty when nothing more follows.
	Next SessionID `json:"next,omitempty"`
	// Seq is the room's newest event when the list was read; live events after it update the list.
	Seq int64 `json:"seq"`
}

type WebLoginRequest struct{}
type WebLoginResponse struct {
	URL string `json:"url"`
}

type AccountsQuotaRequest struct{}
type AccountsQuotaResponse struct {
	Accounts []AccountUsage `json:"accounts"`
}

// UsageCause is empty when usage was read successfully.
type UsageCause string

const (
	UsageOK                  UsageCause = ""
	UsageSignInExpired       UsageCause = "sign_in_expired"
	UsageUnreachable         UsageCause = "unreachable"
	UsageRateLimited         UsageCause = "rate_limited"
	UsageInsufficientBalance UsageCause = "insufficient_balance"
)

type AccountUsage struct {
	AccountID  AccountID     `json:"accountId,omitempty"`
	ObservedAt *time.Time    `json:"observedAt,omitempty"`
	Provider   string        `json:"provider"`
	Label      string        `json:"label"`
	Windows    []UsageWindow `json:"windows"`
	// Active marks an account in use: the one routing would pick next for its provider,
	// from the proxy's own order, or one an open session chose. A spent account is neither.
	Active bool `json:"active"`
	// Balances are prepaid amounts for display only. They carry no meter or alert.
	Balances []UsageBalance `json:"balances,omitempty"`
	Cause    UsageCause     `json:"cause"`
}
type UsageWindowKind string

const (
	UsageWindowFiveHour UsageWindowKind = "five_hour"
	UsageWindowWeekly   UsageWindowKind = "weekly"
	UsageWindowMonthly  UsageWindowKind = "monthly"
	UsageWindowOther    UsageWindowKind = "other"
)

type UsageWindow struct {
	Name        string          `json:"name"`
	Kind        UsageWindowKind `json:"kind"`
	Model       string          `json:"model,omitempty"`
	UsedPercent float64         `json:"usedPercent"`
	Seconds     int64           `json:"seconds"`
	ResetsAt    time.Time       `json:"resetsAt"`
}

// UsageBalance keeps the provider's decimal text, so no currency is converted or rounded.
type UsageBalance struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// HostAddRequest adds a host. Its switches start with agents on unless Agents is false, and CI on unless CI is false or the
// host is the machine the engine runs on; an explicit CI wins.
type HostAddRequest struct {
	Rooms          []RoomID `json:"rooms,omitempty"`
	Name           HostName `json:"name"`
	SSH            string   `json:"ssh"`
	OwnerOnlyAdmin bool     `json:"ownerOnlyAdmin"`
	Agents         *bool    `json:"agents,omitempty"`
	CI             *bool    `json:"ci,omitempty"`
}

// CIOn is the ci switch a new host starts with.
func (r HostAddRequest) CIOn(engineHost bool) bool {
	if r.CI != nil {
		return *r.CI
	}
	return !engineHost
}

type HostAddResponse struct {
	NoSwap bool `json:"noSwap"`
}

// HostSetLimitsRequest replaces the rooms a host serves. No rooms means every room, as for an account.
// Boxes already on the host stay there. A room being deleted is refused.
type HostSetLimitsRequest struct {
	Host  HostName `json:"host"`
	Rooms []RoomID `json:"rooms"`
}
type HostSetLimitsResponse struct{}

// HostSetRequest turns a host's two switches, each on its own; a nil one stays as it is. With agents off no new session box
// lands on the host, and with CI off the CI pool starts no runner there, though a repository still names it. Both off
// disables the host and keeps it registered, as before. A host added exec only (HostAddRequest.ExecOnly) stays exec only
// whatever its switches say until agents or ci is turned on, which runs the full host check first and ends exec only. What
// already runs there stays until it stops.
type HostSetRequest struct {
	Host               HostName `json:"host"`
	Agents             *bool    `json:"agents,omitempty"`
	CI                 *bool    `json:"ci,omitempty"`
	ClearCreateProblem bool     `json:"clearCreateProblem,omitempty"`
	OwnerOnlyAdmin     bool     `json:"ownerOnlyAdmin,omitempty"` // for an exec-only host that agents or ci turn into a full one: the owner confirms exclusive administration, as host add does
}
type HostSetResponse struct{}

// HostListRequest asks for the hosts an owner terminal can open (5.15). Only the owner calls it.
type HostListRequest struct{}
type HostListResponse struct {
	Hosts []HostSummary `json:"hosts"` // ControlPlane first, then each added host by name
}

type RoomShared struct {
	Room  RoomID `json:"room"`
	Bytes int64  `json:"bytes"`
}

// HostSession is a session whose live box a host holds, whatever that box's state.
type HostSession struct {
	Start    *BoxStartStatus `json:"start,omitempty"`
	Room     RoomID          `json:"room"`
	ID       SessionID       `json:"id"`
	Name     string          `json:"name"`
	BoxState BoxState        `json:"boxState"`
}

// HostProblem identifies a durable admission problem independently of reachability.
type HostProblem string

const (
	HostProblemCreateFailed HostProblem = "create_failed"
	// HostProblemShareUnavailable says the host cannot bring up the shared folder's FUSE mount, so it takes no session box.
	HostProblemShareUnavailable HostProblem = "share_unavailable"
)

// RunnerImageProblem says a room's ci-runner image failed to build on a host, so the jobs of its repositories there run on the
// shared image. Reason ends with the build's last lines.
type RunnerImageProblem struct {
	Room   RoomID `json:"room"`
	Image  string `json:"image"` // the saved image's id
	Reason string `json:"reason"`
}

type HostSummary struct {
	Health       HostHealth           `json:"health,omitempty"`
	Problem      HostProblem          `json:"problem,omitempty"`
	RunnerImages []RunnerImageProblem `json:"runnerImages,omitempty"` // runner images that failed to build here
	Sessions     []HostSession        `json:"sessions,omitempty"`     // in rooms the caller can read
	Shared       []RoomShared         `json:"shared"`                 // Every room's folder is on the control plane, so only its summary lists any.
	Leftovers    int                  `json:"leftovers,omitempty"`    // boxes and room folders of deleted rooms that this host still holds, removed when it returns
	Name         HostName             `json:"name"`
	Reachable    bool                 `json:"reachable"`
	Boxes        int                  `json:"boxes"`
	CPUs         int                  `json:"cpus"`
	MemBytes     int64                `json:"memBytes"`
	CPUUsed      float64              `json:"cpuUsed"`
	MemUsed      int64                `json:"memUsed"`
	DiskBytes    int64                `json:"diskBytes"`
	DiskUsed     int64                `json:"diskUsed"`
	DiskLow      bool                 `json:"diskLow"`
	EngineHost   bool                 `json:"engineHost,omitempty"` // this added host is the machine the engine runs on
	Pool         string               `json:"pool,omitempty"`       // the pool that holds this node
	Agents       bool                 `json:"agents,omitempty"`     // new session boxes may land on this added host
	CI           bool                 `json:"ci,omitempty"`         // the CI pool may start runners on this added host
	ExecOnly     bool                 `json:"execOnly,omitempty"`   // added exec only: host-exec runs, no boxes or runners
	Runners      int                  `json:"runners,omitempty"`    // the CI runners on this added host now
	Rooms        []RoomID             `json:"rooms,omitempty"`      // the rooms this added host serves (host.set_limits); none means every room
	GPUs         []GPU                `json:"gpus,omitempty"`       // its cards, each with the VRAM boxes have requested of it
	LastSeen     time.Time            `json:"lastSeen,omitzero"`
	History      []HostLoadPoint      `json:"history"`
}

type HostLoadPoint struct {
	At      time.Time `json:"at"`
	CPUUsed float64   `json:"cpuUsed"`
	MemUsed int64     `json:"memUsed"`
}

type RoomCreateRequest struct {
	Name        string      `json:"name"`
	RootProfile ProfileName `json:"rootProfile"`
}
type RoomCreateResponse struct {
	Room RoomID `json:"room"`
}

// RoomDeleteRequest deletes a room and all its data. Discard must be set, as the deletion saves nothing of the sessions' unpushed work.
type RoomDeleteRequest struct {
	Room    RoomID `json:"room"`
	Discard bool   `json:"discard"`
}

type RoomDeleteResponse struct{}
type RoomRepoAddRequest struct {
	Room    RoomID   `json:"room"`
	Repo    RepoName `json:"repo"`
	Default bool     `json:"default"`
}
type RoomRepoAddResponse struct{}
type RoomReposRequest struct {
	Room RoomID `json:"room"`
}

// RepoCISetRequest turns a repository's CI runners on or off. Max bounds its runners across sizes, and zero leaves the
// number to admission.
type RepoCISetRequest struct {
	Repo RepoName `json:"repo"`
	On   bool     `json:"on"`
	Max  int      `json:"max,omitempty"`
}
type RepoCISetResponse struct{}
type RoomReposResponse struct {
	Repos []RoomRepo `json:"repos"`
}
type SessionStartRequest struct {
	ClientID string      `json:"clientId,omitempty"`
	Room     RoomID      `json:"room"`
	Name     string      `json:"name"`
	Parent   SessionID   `json:"parent,omitempty"`
	Repo     RepoName    `json:"repo,omitempty"`
	Profile  ProfileName `json:"profile"`
	Goal     string      `json:"goal,omitempty"`
	DoneWhen string      `json:"doneWhen,omitempty"`
	Brief    string      `json:"brief,omitempty"`
	Size     BoxSizeName `json:"size,omitempty"`
	SameHost bool        `json:"sameHost,omitempty"`
	GPU      GPUGiB      `json:"gpu,omitempty" desc:"GiB of GPU memory the box needs on one card; true means 1. The session waits for a host with a card that has that much unclaimed. GPU memory is not limited per box, so a box that uses more makes other boxes on the card fail their CUDA allocations, and nothing stops it. Fails with no_gpu when no host reports a GPU and gpu_too_large when no card is big enough."`
	Compose  string      `json:"compose,omitempty"`
}
type SessionStartResponse struct {
	Session SessionID `json:"session"`
	Box     BoxID     `json:"box"`
}

// ComposeLevel is where a compose file lives.
type ComposeLevel string

const (
	ComposeGlobal ComposeLevel = "global"
	ComposeRoom   ComposeLevel = "room"
	ComposeRepo   ComposeLevel = "repo"
)

type ComposeListRequest struct {
	Room RoomID   `json:"room"`
	Repo RepoName `json:"repo,omitempty"`
}

// ComposeFile is one name a session can start with, at the level that wins. Problem says why starting with it would be refused.
type ComposeFile struct {
	Name    string       `json:"name"`
	Level   ComposeLevel `json:"level"`
	Problem string       `json:"problem,omitempty"`
}
type ComposeListResponse struct {
	Files []ComposeFile `json:"files"`
}
type SessionMoveRequest struct {
	Session   SessionID `json:"session"`
	NewParent SessionID `json:"newParent"`
}
type SessionMoveResponse struct{}

type SessionCloseRequest struct {
	Session SessionID `json:"session"`
}

// SessionCloseResponse says what the close does with what its sessions hold. A close takes the session's subtree along.
type SessionCloseResponse struct {
	To     SessionID        `json:"to"`     // the parent that takes the work
	Handed []Responsibility `json:"handed"` // owed work that moves to To
	Unheld []GoalID         `json:"unheld"` // goals that have no holder after the close, because To is root, which holds none
	Ended  []Responsibility `json:"ended"`  // what stops or is withdrawn with the close
}

type SessionSetProfileRequest struct {
	Session SessionID   `json:"session"`
	Profile ProfileName `json:"profile"`
}
type SessionSetProfileResponse struct{}

// SessionSetBranchRequest makes Branch the session's branch on its repository, as the owner's way to move a live session to a
// new name. It adds a record and renames nothing on GitHub. A box cannot call it.
type SessionSetBranchRequest struct {
	Session SessionID `json:"session"`
	Branch  string    `json:"branch"`
}
type SessionSetBranchResponse struct{}
type SessionRestartRequest struct {
	Session SessionID `json:"session"`
}
type SessionRestartResponse struct{}

// SessionRefreshRequest names one session, or a room for every open session in it that the caller may manage.
type SessionRefreshRequest struct {
	Session SessionID `json:"session,omitempty"`
	Room    RoomID    `json:"room,omitempty"`
}

// SessionRefreshResponse lists the sessions marked. A box that is not running needs no mark, so it is not among them.
type SessionRefreshResponse struct {
	Marked []SessionID `json:"marked"`
}
type PostRequest struct {
	DataAttachment *int         `json:"-"` // filled by the owning post boundary, never trusted from a caller
	Data           JSONData     `json:"data,omitempty"`
	Room           RoomID       `json:"room"`
	Text           string       `json:"text"`
	Mentions       []string     `json:"mentions,omitempty"`
	Home           Conversation `json:"home,omitempty"`
	ReplyTo        EventID      `json:"replyTo,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	ClientID       string       `json:"clientId,omitempty"`     // at most 64 characters; a repeat from the same author in a room returns the first post
	ToolCallID     string       `json:"toolCallId,omitempty"`   // Pi transcript call that sent this post
	VisualUpdate   EventID      `json:"visualUpdate,omitempty"` // original visual ID, for a new version
	VisualReply    EventID      `json:"visualReply,omitempty"`  // visual version this post answers
	Forward        EventID      `json:"forward,omitempty"`      // a post to quote under "Forwarded from"; Text is the sender's note
}
type PostResponse struct {
	Event EventID `json:"event"`
	Seq   int64   `json:"seq"`
}
type SessionSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}
type SearchSession struct {
	ID     SessionID `json:"id"`
	Name   string    `json:"name"`
	Room   RoomID    `json:"room"`
	Closed bool      `json:"closed"`
}
type SessionSearchResponse struct {
	Sessions []SearchSession `json:"sessions"`
	Total    int             `json:"total"`
}
type PostSearchRequest struct {
	Query  string `json:"query"`
	Room   RoomID `json:"room,omitempty"`
	Before int64  `json:"before,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// SearchPost is one matching post. Source, when set, names the author of a cross-room post as it was when posted;
// clients prefer it over Names, which resolves only authors whose session still exists in this store.
type SearchPost struct {
	Source *PostSource  `json:"source,omitempty"`
	Seq    int64        `json:"seq"`
	Room   RoomID       `json:"room"`
	Home   Conversation `json:"home"`
	Author Principal    `json:"author"`
	Time   time.Time    `json:"time"`
	Text   string       `json:"text"`
}
type PostSearchResponse struct {
	Names map[SessionID]string `json:"names"`
	Posts []SearchPost         `json:"posts"`
	Total int                  `json:"total"`
}
type EventsListRequest struct {
	Event     EventID     `json:"event,omitempty"` // return this room event by ID
	Room      RoomID      `json:"room"`
	AfterSeq  int64       `json:"afterSeq"`
	BeforeSeq int64       `json:"beforeSeq,omitempty"`
	Limit     int         `json:"limit"`
	Types     []EventType `json:"types,omitempty"`
	// Home, a DM, keeps only events whose home is that DM: its posts and the turns that answered in it.
	Home *Conversation `json:"home,omitempty" desc:"home narrows the log to one owner DM: {kind: dm, session: ses_…}."`
}
type EventsListResponse struct {
	Events []Event              `json:"events"`
	Names  map[SessionID]string `json:"names"`
	Next   int64                `json:"next"`
}
type BoxHelloRequest struct{}
type ModelProvider string

const (
	ModelProviderAnthropic ModelProvider = "anthropic"
	ModelProviderOpenAI    ModelProvider = "openai"
)

type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
)

type ModelChoice struct {
	Provider ModelProvider `json:"provider"`
	ID       string        `json:"id"`
	Thinking ThinkingLevel `json:"thinking"`
}

type BoxRepo struct {
	Name   RepoName `json:"name"`
	Cache  string   `json:"cache"`
	Branch string   `json:"branch"`
	Base   string   `json:"base"`
	Commit string   `json:"commit,omitempty"` // a crew's fixed commit: a new branch starts here, not at Base
}

type TextOnlyModel struct {
	Provider ModelProvider `json:"provider"`
	Model    string        `json:"model"`
}

type BoxHelloResponse struct {
	Room          RoomID `json:"room"`
	Root          bool   `json:"root,omitempty"` // the session has no parent, so add-ons marked root-only reach its box
	GitName       string `json:"gitName,omitempty"`
	GitEmail      string `json:"gitEmail,omitempty"`
	CompactTokens int    `json:"compactTokens,omitempty"`
	Instructions  string `json:"instructions"`
	// ModelInstructions is the model-specific prompt part, replaced with the model at a turn boundary.
	ModelInstructions string `json:"modelInstructions,omitempty"`
	// WorkerInstructions is the part of the prompt a workflow worker shares with every agent. A crew's box puts it before the crew paragraph.
	WorkerInstructions string           `json:"workerInstructions,omitempty"`
	Session            SessionID        `json:"session"`
	Box                BoxID            `json:"box"`
	Model              ModelChoice      `json:"model"`
	TextOnlyModels     []TextOnlyModel  `json:"textOnlyModels,omitempty"`
	Repos              []BoxRepo        `json:"repos,omitempty"`
	Transcripts        []TranscriptFile `json:"transcripts,omitempty"`
	Workflow           *BoxWorkflow     `json:"workflow,omitempty"` // a crew's box runs this script and no Pi of its own
	// GitHub says the engine serves GitHub's read-only MCP tools at /github/ on the box socket, with its own token.
	GitHub bool `json:"github,omitempty"`
}
type TurnOutputRequest struct {
	DataAttachment *int         `json:"-"`
	Data           JSONData     `json:"data,omitempty"`
	Attachments    []Attachment `json:"-"`
	ShellJobs      int          `json:"shellJobs"`
	Jobs           []ShellJobID `json:"jobs,omitempty"` // the jobs ShellJobs counts
	Error          string       `json:"error,omitempty"`
	Cause          FailureCause `json:"cause,omitempty"`
	Turn           TurnID       `json:"turn"`
	Outcome        TurnOutcome  `json:"outcome"`
	Final          string       `json:"final,omitempty"`
	EndPosts       []EventID    `json:"endPosts,omitempty"` // the posts that ended the turn; a completed turn with these has no final text
	Model          string       `json:"model"`
	Usage          Usage        `json:"usage"`
	// Unread names inputs the turn ended without giving to Pi, because their files were still on the way. The next turn takes them.
	Unread []DeliverySeq `json:"unread,omitempty"`
}
type TurnOutputResponse struct{}

// TurnActivityRequest reports the running turn's latest tool call. The box agent sends at most
// one a second. Readers show a label only while its turn runs.
type TurnActivityRequest struct {
	Turn  TurnID       `json:"turn"`
	Kind  ActivityKind `json:"kind,omitempty"` // empty from a box agent that predates kinds, which the engine reads as a tool call
	Label string       `json:"label"`
}
type TurnActivityResponse struct{}
type SessionWorkRequest struct {
	Session SessionID `json:"session"`
	Before  TurnID    `json:"before,omitempty"`
	Limit   int       `json:"limit,omitempty"`
}
type SessionWorkTurn struct {
	Started Event   `json:"started"`
	Ended   *Event  `json:"ended,omitempty"`
	Inputs  []Event `json:"inputs"`
	Posts   []Event `json:"posts"`
}
type SessionWorkResponse struct {
	Turns []SessionWorkTurn `json:"turns"`
	More  bool              `json:"more"`
	Total int               `json:"total"`
}

// TranscriptReadRequest reads a session's turns. Step reads one crew worker instead, by its journal key: the one turn its
// file belongs to, with the prompt as the input and the step's own times.
type TranscriptReadRequest struct {
	Session   SessionID      `json:"session"`
	Step      string         `json:"step,omitempty"`
	Turn      TurnID         `json:"turn,omitempty"`
	Before    TurnID         `json:"before,omitempty"`
	Limit     int            `json:"limit,omitempty"`
	Item      string         `json:"item,omitempty"`
	Part      TranscriptPart `json:"part,omitempty"`
	Offset    int            `json:"offset,omitempty"`
	AfterItem string         `json:"afterItem,omitempty"`
	Page      TranscriptPage `json:"page,omitempty"`
	Cursor    int            `json:"cursor,omitempty"`
	Tail      bool           `json:"tail,omitempty"`
	PostIndex int            `json:"postIndex,omitempty"`
}

// TranscriptPage selects one kind of turn continuation.
type TranscriptPage string

const (
	TranscriptPageOlderItems     TranscriptPage = "older_items"
	TranscriptPageMandatoryItems TranscriptPage = "mandatory_items"
	TranscriptPageMandatoryPosts TranscriptPage = "mandatory_posts"
)

type TranscriptPart string

const (
	TranscriptPartOutput    TranscriptPart = "output"
	TranscriptPartText      TranscriptPart = "text"
	TranscriptPartArguments TranscriptPart = "arguments"
	TranscriptPartScript    TranscriptPart = "script"
	TranscriptPartDiff      TranscriptPart = "diff"
)

type TranscriptItemKind string

const (
	TranscriptText     TranscriptItemKind = "text"
	TranscriptThinking TranscriptItemKind = "thinking"
	TranscriptTool     TranscriptItemKind = "tool"
	TranscriptPost     TranscriptItemKind = "post"
	TranscriptTooLarge TranscriptItemKind = "too_large"
	TranscriptSteer    TranscriptItemKind = "steer" // a message a running turn read, at the place it read it
	// TranscriptCompaction marks where Pi summarized the conversation, whatever asked it to. Tokens is the context size before.
	TranscriptCompaction TranscriptItemKind = "compaction"
	// TranscriptAttemptFailed stands where a model call failed and Pi dropped its message from the model's context. ErrorName is the name of the failure Pi recorded, when it recorded one.
	TranscriptAttemptFailed TranscriptItemKind = "attempt_failed"
)

type TranscriptCallStatus string

const (
	TranscriptCallRunning   TranscriptCallStatus = "running"
	TranscriptCallOK        TranscriptCallStatus = "ok"
	TranscriptCallError     TranscriptCallStatus = "error"
	TranscriptCallCancelled TranscriptCallStatus = "cancelled"
)

type TranscriptCall struct {
	Tool       string               `json:"tool"`
	Target     string               `json:"target,omitempty"`
	Status     TranscriptCallStatus `json:"status"`
	DurationMs *int64               `json:"durationMs,omitempty"`
	Error      string               `json:"error,omitempty"`
}

// PostDataRef keeps oversized transcript values readable without defeating transcript page budgets.
type PostDataRef struct {
	File    Attachment `json:"file"`
	Summary string     `json:"summary"`
}

type TranscriptItem struct {
	DataAttachment *int                    `json:"dataAttachment,omitempty"`
	DataRef        *PostDataRef            `json:"dataRef,omitempty"`
	Data           JSONData                `json:"data,omitempty"`
	ID             string                  `json:"id"`
	Index          int                     `json:"index,omitempty"`
	Turn           TurnID                  `json:"turn"`
	Kind           TranscriptItemKind      `json:"kind"`
	At             time.Time               `json:"at"`
	Ended          *time.Time              `json:"ended,omitempty"`
	Type           EventType               `json:"type,omitempty"` // a steer's event; Text is its body as for a TranscriptInput
	Text           string                  `json:"text,omitempty"`
	Tool           string                  `json:"tool,omitempty"`
	Arguments      string                  `json:"arguments,omitempty"`
	Script         string                  `json:"script,omitempty"`
	Calls          []TranscriptCall        `json:"calls,omitempty"`
	CallsOmitted   int                     `json:"callsOmitted,omitempty"`
	Output         string                  `json:"output,omitempty"`
	Diff           string                  `json:"diff,omitempty"`
	Failed         bool                    `json:"failed,omitempty"`
	More           bool                    `json:"more,omitempty"`
	Event          EventID                 `json:"event,omitempty"`
	Room           RoomID                  `json:"room,omitempty"`
	From           *Principal              `json:"from,omitempty"`
	Seq            int64                   `json:"seq,omitempty"`
	Home           *Conversation           `json:"home,omitempty"`
	Bytes          int64                   `json:"bytes,omitempty"`
	Tokens         int64                   `json:"tokens,omitempty"`
	ErrorName      string                  `json:"errorName,omitempty"` // an attempt_failed item's failure, such as SocketError
	Attachments    []Attachment            `json:"attachments,omitempty"`
	Image          *TranscriptImage        `json:"image,omitempty"`
	Posts          []TranscriptItem        `json:"posts,omitempty"`
	PostsOmitted   int                     `json:"postsOmitted,omitempty"`
	PostsNext      *TranscriptPostLocation `json:"postsNext,omitempty"`
}

// TranscriptImage marks a read that returned an image. The bytes stay in the transcript, and /v1/files/transcript serves them.
type TranscriptImage struct {
	Item  string `json:"item"`
	Media string `json:"media"`
	Bytes int64  `json:"bytes"`
}

// Session-origin posts can only use a room feed; post_api rejects DM homes.
type TranscriptPostLocation struct {
	Room RoomID `json:"room"`
	Seq  int64  `json:"seq"`
}
type TranscriptInput struct {
	DataAttachment *int          `json:"dataAttachment,omitempty"`
	DataRef        *PostDataRef  `json:"dataRef,omitempty"`
	Data           JSONData      `json:"data,omitempty"`
	Source         *PostSource   `json:"source,omitempty"`
	Home           *Conversation `json:"home,omitempty"`
	At             time.Time     `json:"at"`
	ID             EventID       `json:"id"`
	Seq            int64         `json:"seq"`
	Room           RoomID        `json:"room"`
	Type           EventType     `json:"type"`
	From           Principal     `json:"from"`
	Text           string        `json:"text"`
	More           bool          `json:"more,omitempty"`
	Attachments    []Attachment  `json:"attachments,omitempty"`
}
type TranscriptTurn struct {
	ID                TurnID            `json:"id"`
	Started           time.Time         `json:"started"`
	Ended             *time.Time        `json:"ended,omitempty"`
	DurationMS        int64             `json:"durationMs,omitempty"`
	Outcome           TurnOutcome       `json:"outcome,omitempty"`
	Cause             FailureCause      `json:"cause,omitempty"`
	Inputs            []TranscriptInput `json:"inputs"`
	Items             []TranscriptItem  `json:"items"`
	More              bool              `json:"more"`
	After             string            `json:"after,omitempty"`
	NextItem          int               `json:"nextItem,omitempty"`
	OlderItems        bool              `json:"olderItems,omitempty"`
	MandatoryItems    bool              `json:"mandatoryItems,omitempty"`
	NextMandatory     int               `json:"nextMandatory,omitempty"`
	MandatoryPosts    bool              `json:"mandatoryPosts,omitempty"`
	NextMandatoryPost int               `json:"nextMandatoryPost,omitempty"`
	Tail              bool              `json:"tail,omitempty"`
	NextPost          int               `json:"nextPost,omitempty"`
}
type TranscriptStep struct {
	Turn    TurnID           `json:"turn"`
	Tool    string           `json:"tool"`
	Label   string           `json:"label"`
	Started time.Time        `json:"started"`
	Ended   *time.Time       `json:"ended,omitempty"`
	Failed  bool             `json:"failed"`
	Preview string           `json:"preview"`
	Image   *TranscriptImage `json:"image,omitempty"`
}
type TranscriptReadResponse struct {
	Steps      []TranscriptStep `json:"steps"`
	Truncated  bool             `json:"truncated"`
	Turns      []TranscriptTurn `json:"turns"`
	Before     TurnID           `json:"before,omitempty"`
	More       bool             `json:"more"`
	Chunk      string           `json:"chunk,omitempty"`
	NextOffset int              `json:"nextOffset,omitempty"`
}
type TranscriptAppendRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Bytes  []byte `json:"bytes"`
}
type TranscriptAppendResponse struct {
	AckedOffset int64 `json:"ackedOffset"`
}
type ShellJobEndKind string

const (
	ShellJobExited   ShellJobEndKind = "exit"
	ShellJobSignaled ShellJobEndKind = "signal"
	ShellJobStopped  ShellJobEndKind = "stopped"
)

type ShellJobEnd struct {
	Kind   ShellJobEndKind `json:"kind"`
	Code   int             `json:"code,omitempty"`
	Signal string          `json:"signal,omitempty"`
}

type ShellJobEndedRequest struct {
	Description string       `json:"description,omitempty"`
	End         *ShellJobEnd `json:"end,omitempty"`
	Job         ShellJobID   `json:"job"`
	ExitCode    int          `json:"exitCode"` // unread; box agents still send it and requests decode strictly
	Tail        string       `json:"tail"`     // the last output, masked
	// Silent says the session asked for the end with job stop. The engine takes the job off what the session holds and sends no notice.
	Silent bool `json:"silent,omitempty"`
}
type ShellJobEndedResponse struct{}

// ShellJobRunningRequest reports a background job still running at a mark of its running time.
type ShellJobRunningRequest struct {
	Description  string     `json:"description,omitempty"`
	Job          ShellJobID `json:"job"`
	RanMinutes   int        `json:"ranMinutes"`
	QuietMinutes *int       `json:"quietMinutes,omitempty"` // since the last output; absent before any
	Tail         string     `json:"tail"`                   // the last whole lines of output, masked
}
type ShellJobRunningResponse struct{}
type TurnInput struct {
	Delivery DeliverySeq  `json:"delivery"`
	Surface  Surface      `json:"surface"`
	Reply    ReplyChannel `json:"reply"`
	Text     string       `json:"text"`
	Files    []TurnFile   `json:"files,omitempty"` // the attachments the frame names
}

// TurnFile is one attachment of an input. The box fetches it by hash and stores it at Path before Pi reads the input.
type TurnFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}
type TurnStart struct {
	Sections          []ContextSection `json:"sections"`
	Turn              TurnID           `json:"turn"`
	Inputs            []TurnInput      `json:"inputs"`
	Model             ModelChoice      `json:"model"` // the session profile's model when the turn was dispatched
	ModelInstructions string           `json:"modelInstructions,omitempty"`
	// CompactTokens is the profile's compaction trigger, which applies with Model. 0: Pi's default.
	CompactTokens int `json:"compactTokens,omitempty"`
}
type TurnQueue string

const (
	QueueSteer    TurnQueue = "steer"
	QueueFollowUp TurnQueue = "follow_up"
)

type TurnSteer struct {
	Input     TurnInput  `json:"input"`
	Queue     TurnQueue  `json:"queue"`
	Interrupt *Interrupt `json:"interrupt,omitempty"` // actual sender checked by the engine against the receiving session's tree
}
type TurnStop struct {
	Turn   TurnID `json:"turn"`
	Reason string `json:"reason"`
}

// ComputerStartRequest starts the desktop in a session's box, for the owner's view: the display, its window manager and
// Chromium. It returns once the desktop runs. The view then carries the desktop's VNC stream on GET
// /v1/computers/stream?session=, a WebSocket of binary frames both ways.
type ComputerStartRequest struct {
	Session SessionID `json:"session"`
}
type ComputerStartResponse struct{}

// ShellWatchStart asks the box agent to post each shell job's output with shell.output while the
// owner watches. Each start begins a new generation, and its first post resets every job's view.
type ShellWatchStart struct {
	Generation uint64 `json:"generation"`
}
type ShellWatchStop struct{}

// ShellOutputRequest carries the new output of the box's shell jobs, about every 250 ms while watched.
// The engine drops a post from an older generation.
type ShellOutputRequest struct {
	Generation uint64           `json:"generation"`
	Jobs       []ShellJobOutput `json:"jobs"`
}
type ShellOutputResponse struct{}

// ShellJobOutput is one job's output since the last post, masked, in whole lines. With Reset, Text is
// the job's last 64 KiB and replaces what the viewer shows. GET /v1/shell/watch streams these values.
type ShellJobOutput struct {
	Description string       `json:"description,omitempty"`
	Host        HostName     `json:"host,omitempty"` // the machine a host run is on
	End         *ShellJobEnd `json:"end,omitempty"`
	Job         ShellJobID   `json:"job"`
	Command     string       `json:"command"`
	Running     bool         `json:"running"`
	ExitCode    int          `json:"exitCode"` // when Running is false
	Text        string       `json:"text"`
	Reset       bool         `json:"reset,omitempty"`
	Started     time.Time    `json:"started"`        // by the box's clock
	Ended       time.Time    `json:"ended,omitzero"` // by the box's clock, once the job has ended
}

// TerminalResize is the JSON text frame a client sends on GET /v1/terminal to set the terminal's size.
// Binary frames carry the terminal's bytes both ways.
type TerminalResize struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// TerminalExit is the engine's last JSON text frame on GET /v1/terminal, sent when the shell exits.
type TerminalExit struct {
	ExitCode int `json:"exitCode"`
}

type ComputerChangedRequest struct {
	To ComputerState `json:"to"`
}
type ComputerChangedResponse struct{}

// SaveDiff asks a closing box to write what it has not pushed to the room's shared folder, once. Name is the session's name,
// which the file is named after.
type SaveDiff struct {
	Name string `json:"name"`
}

// DiffSavedRequest is the box's one report of that step. File is empty when nothing was unpushed or nothing was saved, and Error
// says why not in the second case.
type DiffSavedRequest struct {
	File    string   `json:"file,omitempty"`   // as the box sees it, under /box/shared
	Commits int      `json:"commits"`          // unpushed commits in the file
	Files   int      `json:"files"`            // files its diffs change
	Capped  bool     `json:"capped,omitempty"` // a section was over the size cap and holds the commit list and stat only
	Failed  []string `json:"failed,omitempty"` // worktrees that could not be saved, each with its reason; the others are in the file
	Stale   []string `json:"stale,omitempty"`  // repositories whose fetch failed, each with its reason; their figures come from the box's last fetch and may be stale
	Error   string   `json:"error,omitempty"`
}
type DiffSavedResponse struct{}

// CloseNotice tells a closing session's parent what became of its unpushed work. A nil report is a box that was not asked or did
// not answer.
func (saved *DiffSavedRequest) CloseNotice(session string) string {
	var text string
	switch {
	case saved == nil:
		return fmt.Sprintf("Session %s closed; not checked, nothing saved (its box was not running, or did not report in time). Anything it had not pushed is gone.", session)
	case saved.Error != "":
		return fmt.Sprintf("Session %s closed; no diff was saved (%s). Anything it had not pushed is gone.", session, saved.Error)
	case saved.File == "":
		return fmt.Sprintf("Session %s closed; nothing was unpushed.", session)
	case saved.Capped:
		text = fmt.Sprintf("Session %s closed; its unpushed work, %d commits and %d changed files, is in %s. A diff over 10 MiB is left out of it, which then holds the commit list and a file summary.", session, saved.Commits, saved.Files, saved.File)
	default:
		text = fmt.Sprintf("Session %s closed; its unpushed work, %d commits and %d changed files, is in %s.", session, saved.Commits, saved.Files, saved.File)
	}
	if len(saved.Failed) > 0 {
		text += " Not saved: " + strings.Join(saved.Failed, "; ") + "."
	}
	if len(saved.Stale) > 0 {
		text += " The remote could not be fetched for " + strings.Join(saved.Stale, "; ") + ", so these figures come from the box's last fetch and may be stale: work pushed or merged since may be counted as unpushed."
	}
	return text
}

// Compact asks a quiet box to compact before it stops, if its context is over OverTokens. Turn is the session's last
// turn: it names the quiet period, and comes back in BoxCompactedRequest.
type Compact struct {
	Turn       TurnID `json:"turn"`
	OverTokens int64  `json:"overTokens"`
}

// BoxCompactedRequest answers Compact. Neither Compacted nor Error means the context was small enough to leave alone.
type BoxCompactedRequest struct {
	Turn         TurnID `json:"turn"`
	Compacted    bool   `json:"compacted,omitempty"`
	Error        string `json:"error,omitempty"`
	TokensBefore int64  `json:"tokensBefore,omitempty"`
	TokensAfter  int64  `json:"tokensAfter,omitempty"`
	Usage        Usage  `json:"usage"`
}
type BoxCompactedResponse struct{}

type DeliverySeq int64
type DeliveryDoneRequest struct {
	Delivery DeliverySeq `json:"delivery"`
}
type DeliveryDoneResponse struct{}

type BoxMessageType string
type BoxMessageBody interface {
	BoxMessageType() BoxMessageType
	isBoxMessageBody()
}

// ErrUnknownBoxMessage wraps the decode error for a message type this build does not know, so an older box
// can skip a message from a newer engine.
var ErrUnknownBoxMessage = errors.New("unknown box message type")

type BoxMessage struct {
	Body BoxMessageBody `json:"body"`
}

func (ShellWatchStart) BoxMessageType() BoxMessageType { return "shell.watch.start" }
func (ShellWatchStart) isBoxMessageBody()              {}
func (ShellWatchStop) BoxMessageType() BoxMessageType  { return "shell.watch.stop" }
func (ShellWatchStop) isBoxMessageBody()               {}

func (SaveDiff) BoxMessageType() BoxMessageType { return "save_diff" }
func (SaveDiff) isBoxMessageBody()              {}

func (TurnStart) BoxMessageType() BoxMessageType { return "turn.start" }
func (TurnStart) isBoxMessageBody()              {}
func (TurnSteer) BoxMessageType() BoxMessageType { return "turn.steer" }
func (TurnSteer) isBoxMessageBody()              {}
func (TurnStop) BoxMessageType() BoxMessageType  { return "turn.stop" }
func (TurnStop) isBoxMessageBody()               {}
func (Compact) BoxMessageType() BoxMessageType   { return "compact" }
func (Compact) isBoxMessageBody()                {}

type TranscriptFetchRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
}
type TranscriptFetchResponse struct {
	Bytes []byte `json:"bytes"`
}

type TranscriptFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// ExecOnly reports a host added with agents and CI both off: it takes host-exec runs only and needs just SSH and bash.
func (r HostAddRequest) ExecOnly() bool {
	return r.Agents != nil && !*r.Agents && r.CI != nil && !*r.CI
}

// SessionListPage is how many sessions a session's list returns when it names no limit.
const SessionListPage = 25

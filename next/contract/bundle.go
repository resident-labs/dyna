package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// The migration bundle (spec section 8.1) is the only bridge from v1. The v1
// exporter writes it before v2 exists, so records name each other by their v1
// labels; the import makes the v2 IDs and keeps the labels for display.

// BundleVersion is the bundle format. The import accepts exactly the version
// it knows.
type BundleVersion int

const BundleV1 BundleVersion = 1

// V1 labels, as the owner knows them.
type (
	QuestionLabel string // "q33"
	RoomName      string // "dyna"
	V1Address     string // "dyna:code-architectural", or "owner"
)

// ImportedAuthor is who wrote an imported record. v1 sessions are not
// imported, so the label stays text.
type ImportedAuthor struct {
	Kind  PrincipalKind `json:"kind"` // owner, session or engine
	Label V1Address     `json:"label"`
}

// BundleSource names the v1 state the bundle came from.
type BundleSource struct {
	Commit       string `json:"commit"` // the v1-final commit
	Backup       string `json:"backup"` // the final backup's file name
	BackupSHA256 string `json:"backupSha256"`
}

type BundleCounts struct {
	InstructionChars int `json:"instructionChars"`
	Goals            int `json:"goals"`
	GoalItems        int `json:"goalItems"`
	Issues           int `json:"issues"`
	PRs              int `json:"prs"`
	Questions        int `json:"questions"`
	Subscriptions    int `json:"subscriptions"`
	Hosts            int `json:"hosts"`
	Accounts         int `json:"accounts"`
	Devices          int `json:"devices"`
	Sessions         int `json:"sessions"`
}

// GlobalBundle is global.json.
type GlobalBundle struct {
	Version       BundleVersion   `json:"version"`
	Rooms         []RoomName      `json:"rooms"`
	Exported      time.Time       `json:"exported"`
	Source        BundleSource    `json:"source"`
	Instructions  string          `json:"instructions"`
	Settings      SettingsBundle  `json:"settings"`
	Profiles      []ProfileBundle `json:"profiles"`
	Hosts         []HostBundle    `json:"hosts"`
	Accounts      []AccountLabel  `json:"accounts"`
	Devices       []DeviceBundle  `json:"devices"`
	Repos         []RepoBundle    `json:"repos"`
	Skills        []SkillBundle   `json:"skills"`
	Subscriptions []Subscription  `json:"subscriptions"` // machine scope
	Counts        BundleCounts    `json:"counts"`
}

// RoomBundle is rooms/<name>.json.
type RoomBundle struct {
	Version       BundleVersion    `json:"version"`
	Name          RoomName         `json:"name"`
	Repos         []RoomRepo       `json:"repos"`
	Limits        RoomLimits       `json:"limits"`
	Color         string           `json:"color"`
	Paused        bool             `json:"paused"`
	RootProfile   ProfileName      `json:"rootProfile,omitempty"`
	Upstreams     []RoomUpstream   `json:"upstreams,omitempty"`
	Instructions  string           `json:"instructions"`
	Goals         []GoalBundle     `json:"goals"`
	Issues        []IssueBundle    `json:"issues"`
	NextIssue     int              `json:"nextIssue"`
	PRs           []TrackedPR      `json:"prs"`
	Questions     []QuestionBundle `json:"questions"`
	Subscriptions []Subscription   `json:"subscriptions"` // room and session scope
	Handoff       []SessionHandoff `json:"handoff"`
	HookHandoff   []HookHandoff    `json:"hookHandoff,omitempty"`
	LimitHandoff  []RoomLimitGap   `json:"limitHandoff,omitempty"`
	RepoHandoff   []RepoHandoff    `json:"repoHandoff,omitempty"`
	Counts        BundleCounts     `json:"counts"`
}

// SettingsBundle keeps only the v1 settings that v2 uses.
type SettingsBundle struct {
	OwnerName        string      `json:"ownerName"`
	RootProfile      ProfileName `json:"rootProfile"`
	SubagentMaxDepth int         `json:"subagentMaxDepth"`
	CheckInInterval  DurationMS  `json:"checkInInterval"`
	ShellSilence     DurationMS  `json:"shellSilence"`
}

type ProfileBundle struct {
	Name          ProfileName   `json:"name"`
	Description   string        `json:"description"`
	Model         string        `json:"model"` // a model ID that CLIProxyAPI serves
	Thinking      ThinkingLevel `json:"thinking,omitempty"`
	Models        []string      `json:"models,omitempty"`
	Fallback      ProfileName   `json:"fallback,omitempty"`
	Taste         int           `json:"taste"`
	Intelligence  int           `json:"intelligence"`
	Cost          int           `json:"cost"`
	Diagnostics   int           `json:"diagnostics"`
	CompactTokens int           `json:"compactTokens,omitempty"` // 0: Pi's default
	MaxConcurrent int           `json:"maxConcurrent,omitempty"`
	Default       bool          `json:"default,omitempty"`
}

// HostBundle comes from the v1 machines table. The host check measures
// CPUs, memory and disk again.
type HostBundle struct {
	Name   HostName   `json:"name"`
	SSH    string     `json:"ssh"` // user@addr
	Labels []string   `json:"labels,omitempty"`
	Rooms  []RoomName `json:"rooms,omitempty"` // empty: every room
}

// AccountProvider is one of these or, for a provider the owner added, "custom-<name>".
type AccountProvider string

const (
	ProviderAnthropic  AccountProvider = "anthropic"
	ProviderOpenAI     AccountProvider = "openai"
	ProviderKimi       AccountProvider = "kimi"
	ProviderXAI        AccountProvider = "xai"
	ProviderZAI        AccountProvider = "zai"
	ProviderOpencodeGo AccountProvider = "opencode-go"
	ProviderDeepSeek   AccountProvider = "deepseek"
	ProviderMeta       AccountProvider = "meta"
)

// AccountLabel holds no credential; the owner signs in again in CLIProxyAPI.
type AccountLabel struct {
	Provider AccountProvider `json:"provider"`
	Label    string          `json:"label"`
	Repos    []RepoName      `json:"repos,omitempty"` // empty: every repository
	Rooms    []RoomName      `json:"rooms,omitempty"` // empty: every room
}

type DeviceScope string

type DeviceBundle struct {
	Name        string        `json:"name"`
	TokenSHA256 string        `json:"tokenSha256"`
	Created     time.Time     `json:"created"`
	Expires     time.Time     `json:"expires"`
	Scopes      []DeviceScope `json:"scopes"`
}

type MergePolicy string

const (
	MergeQueue  MergePolicy = "queue"
	MergeManual MergePolicy = "manual"
)

type CIMode string

const (
	CIGitHub CIMode = "github"
	CIBox    CIMode = "box"
)

type AttributionKind string

const (
	AttributionCommit  AttributionKind = "commit"
	AttributionPR      AttributionKind = "pr"
	AttributionComment AttributionKind = "comment"
)

type AttributionLine struct {
	Kind AttributionKind `json:"kind"`
	Line string          `json:"line"`
}

// RepoBundle carries a repository's shared settings.
type RepoBundle struct {
	Repo        RepoName          `json:"repo"`
	DefaultRoom RoomName          `json:"defaultRoom"`
	MergePolicy MergePolicy       `json:"mergePolicy"`
	CIMode      CIMode            `json:"ciMode"`
	Attribution []AttributionLine `json:"attribution,omitempty"`
}

// SkillBundle has exactly one of Body (an owner-made SKILL.md) and Package
// (an installed skill, such as "rea-agents@6.8.0").
type SkillBundle struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
	Package     string `json:"package,omitempty"`
}

type RoomRepo struct {
	Repo    RepoName `json:"repo"`
	Default bool     `json:"default"`
}

// RoomLimits is the room budget; zero is unset.
// RoomUpstream records a v1 issue-watch upstream for manual recreation.
type RoomUpstream struct {
	Repo     RepoName `json:"repo"`
	Upstream string   `json:"upstream"`
}

type RoomLimits struct {
	CPUs     int   `json:"cpus"`
	MemBytes int64 `json:"memBytes"`
}

type ItemState string

const (
	ItemOpen    ItemState = "open"
	ItemDone    ItemState = "done"
	ItemDropped ItemState = "dropped"
)

type GoalItemBundle struct {
	Label    string    `json:"label"` // "i2"
	Text     string    `json:"text"`
	State    ItemState `json:"state"`
	Assignee V1Address `json:"assignee,omitempty"`
}

type GoalNote struct {
	At   time.Time      `json:"at"`
	By   ImportedAuthor `json:"by"`
	Text string         `json:"text"`
}

// GoalBundle is an active goal. It imports with no holder.
type GoalBundle struct {
	Label      GoalID           `json:"label"`
	Text       string           `json:"text"`
	Detail     string           `json:"detail,omitempty"`
	Setter     ImportedAuthor   `json:"setter"`
	Created    time.Time        `json:"created"`
	Amendments []GoalNote       `json:"amendments,omitempty"`
	Items      []GoalItemBundle `json:"items,omitempty"`
}

type IssueFile struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

type IssueNote struct {
	At   time.Time      `json:"at"`
	By   ImportedAuthor `json:"by"`
	Text string         `json:"text"`
}

// IssueBundle is an open issue. Its body and notes come from the v1 feed.
type IssueBundle struct {
	Number    int            `json:"number"` // the D-number
	Title     string         `json:"title"`
	Body      string         `json:"body,omitempty"`
	Files     []IssueFile    `json:"files,omitempty"`
	Filer     ImportedAuthor `json:"filer"`
	Filed     time.Time      `json:"filed"`
	Goal      GoalID         `json:"goal,omitempty"`
	Assignee  V1Address      `json:"assignee,omitempty"`
	GitHubURL string         `json:"githubUrl,omitempty"`
	Notes     []IssueNote    `json:"notes,omitempty"`
}

// TrackedPR records ownership and the branch needed to push after import.
type TrackedPR struct {
	Branch string         `json:"branch,omitempty"`
	Repo   RepoName       `json:"repo"`
	Number int            `json:"number"`
	Author ImportedAuthor `json:"author"`
	Goal   GoalID         `json:"goal,omitempty"`
	Hold   *PRHandoffHold `json:"hold,omitempty"`
}

// PRHandoffHold preserves a v1 merge hold for the room handoff.
type PRHandoffHold struct {
	Reason string         `json:"reason"`
	By     ImportedAuthor `json:"by"`
	At     time.Time      `json:"at"`
}

type QuestionKind string

const (
	QuestionAsk      QuestionKind = "question"
	QuestionTerminal QuestionKind = "terminal"
)

// QuestionBundle is an unanswered question or show_pty request.
type QuestionBundle struct {
	Label     QuestionLabel  `json:"label"`
	Kind      QuestionKind   `json:"kind"`
	Asker     ImportedAuthor `json:"asker"`
	Text      string         `json:"text"`
	Options   []string       `json:"options,omitempty"`
	Meanwhile string         `json:"meanwhile,omitempty"`
	Command   string         `json:"command,omitempty"` // show_pty requests only
	Argv      []string       `json:"argv,omitempty"`
	Cwd       string         `json:"cwd,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Asked     time.Time      `json:"asked"`
}

// RepoHandoff names a repository that v2 cannot import yet.
type RepoHandoff struct {
	Identity string            `json:"identity"`
	Reason   RepoHandoffReason `json:"reason"`
}

type RepoHandoffReason string

const (
	RepoUnsupportedForge   RepoHandoffReason = "unsupportedForge"
	RepoUnverifiedIdentity RepoHandoffReason = "unverifiedIdentity"
	RepoLocalCI            RepoHandoffReason = "localCI"
)

// RoomLimitGap identifies a v1 limit that needs manual review.
type RoomLimitGap string

const RoomLimitMemoryMaxWithoutHigh RoomLimitGap = "memoryMaxWithoutHigh"

// HookHandoff names an active v1 hook that needs manual recreation.
type HookHandoff struct {
	Name   string            `json:"name"`
	Fields []string          `json:"fields"`
	Reason HookHandoffReason `json:"reason,omitempty"`
}

type HookHandoffReason string

const HookCheckoutUnverified HookHandoffReason = "checkoutUnverified"

// SessionHandoff tells root about one session that was open in v1.
type SessionHandoff struct {
	Name           V1Address            `json:"name"`
	Profile        string               `json:"profile"`
	ProfileWarning ProfileHandoffReason `json:"profileWarning,omitempty"`
	Parent         V1Address            `json:"parent,omitempty"`
	Goals          []GoalID             `json:"goals,omitempty"`
	ArchiveRef     string               `json:"archiveRef,omitempty"` // refs/dyna/archive/<session>
	LastPost       string               `json:"lastPost"`             // cut to 2,000 characters
}

type ProfileHandoffReason string

const ProfileUnsupportedMuse ProfileHandoffReason = "unsupportedMuse"

type SubscriptionScope string

const (
	ScopeMachine SubscriptionScope = "machine"
	ScopeRoom    SubscriptionScope = "room"
	ScopeSession SubscriptionScope = "session"
)

// Subscription is a hook as a typed event subscription (spec 8.5). Exactly
// one of On and Every, and exactly one of Wake and Run, is set. A bundle
// can hold Wake subscriptions when a v1 hook maps cleanly.
type Subscription struct {
	ID      SubscriptionID    `json:"id"`
	Scope   SubscriptionScope `json:"scope"`
	Room    RoomID            `json:"room,omitempty"`
	Session SessionID         `json:"session,omitempty"`
	On      []EventType       `json:"on,omitempty"`
	Every   DurationMS        `json:"every,omitempty"`
	Wake    *Principal        `json:"wake,omitempty"`
	Run     *RunSpec          `json:"run,omitempty"`
}

type RunSpec struct {
	Command []string    `json:"command"`
	Image   string      `json:"image"`
	Size    BoxSizeName `json:"size"`
	Files   []RunFile   `json:"files,omitempty"`
}

type RunFile struct {
	Path string `json:"path"` // inside the box
	Body string `json:"body"`
}

func (b GlobalBundle) version() BundleVersion { return b.Version }
func (b RoomBundle) version() BundleVersion   { return b.Version }

// DecodeBundle reads one bundle part. It refuses unknown fields at any
// depth, trailing data and a version it does not know.
func DecodeBundle[T interface {
	GlobalBundle | RoomBundle
	version() BundleVersion
}](r io.Reader) (T, error) {
	var b T
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, fmt.Errorf("decode bundle: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return b, errors.New("decode bundle: trailing data")
	}
	if v := b.version(); v != BundleV1 {
		return b, fmt.Errorf("decode bundle: version %d, want %d", v, BundleV1)
	}
	return b, nil
}

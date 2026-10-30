# Dyna v2 specification

Status: draft for owner approval. Lead: dyna:lead-rewrite. Co-authors: dyna:node-shell (sections 5 and 6) and dyna:code-architectural (sections 7, 8 and 9).

This spec is the authority for the code in `next/`. No product code is written before the owner approves it. A change to this spec is a pull request that the lead approves. A change to a settled decision also needs the owner.

Sources: the rewrite card v8, the migration inventory, the compute v2 plan and its stage 0 report, the reply-channels report, issues D-579 to D-586, the M0 reports (M0, M0b, M0c) and the pi-cloud comparison.

## 0. Decisions this spec adds

The owner settled the design on the card. Writing the spec needed the decisions below, which the card does not cover. Approving the spec approves them.

1. **Room events move into SQLite.** Posts and system events become rows in one `events` table, because delivery, wake rules, the web app and `dyna room log` all query them. Rule r1 keeps room messages as JSONL files today; v2 changes that for events only. Transcripts and large outputs stay files (section 4).
2. **Leads are placement roots.** A cgroup cannot span machines. A child therefore runs on its parent's host. Root's direct children (leads) each get their own budget on any host, and the engine enforces the room limit by accounting. Below each lead, budgets nest as cgroups (section 5.5).
3. **CLIProxyAPI holds every model credential.** It holds the Claude, ChatGPT, Kimi and xAI sign-ins and refreshes them, and it holds the zai, opencode-go and deepseek keys as OpenAI-compatible upstreams (M0c). The engine's own code never reads a model credential and never refreshes a model token; it adds only the proxy's client key. From M3, the proxy keeps its tokens in its own table in `store.db` (5.7). Pi holds none either. Muse is not a v2 model account: its subscription key works only in Meta's own client (M0c). M1 and M2 checks use a copied access token with no refresh token, as M0 did. From M3 on, the owner signs in on the Accounts page (5.12), which drives CLIProxyAPI's own sign-in flows. Each login has its own tokens, so v1's sign-ins keep working. The engine reads quota without any model credential, only through CLIProxyAPI (5.12).
4. **Pi is the only harness.** The owner decided this on 2026-10-30; it replaces the card's "Claude Code and Pi". Claude models go through Pi's `anthropic` provider to the owner's Claude sign-ins in CLIProxyAPI, which presents the requests as Claude Code ("cloaking", M0c). ChatGPT goes through an `openai-responses` provider, because the native Pi `openai-codex` provider did not work through the proxy (M0b). In M0c, Pi with `claude-opus-5-5` passed tool turns, caching, compaction, steer, follow-up and abort. Pi sent effort `high` for every thinking level, so our Pi fork fixes the effort mapping in M1. Section 9.1 lists what we lose from Claude Code.
   - **Accepted risk.** Anthropic's terms allow Claude Free, Pro and Max sign-ins only in Claude Code and Claude.ai, and Anthropic blocks clients that pose as Claude Code. Pi's traffic can therefore get an account flagged, or be billed as extra usage instead of plan limits. The owner accepts this. The self-hosting trial uses one Claude account only, to limit the damage.
5. **A dormant session stops its box.** The container stops, and its volume stays. Its request no longer counts on the host. A wake starts it again (section 5.3).
6. **Only the owner grants host exec.** Root's default grant covers every host, including the control-plane host, with no approval step, as the owner decided. The engine refuses owner-only calls from host-exec processes (section 6).
7. **Root is a position in the tree.** Any session above another may approve its agent-class operations and start, move or close it. Root is above every session, so it can do all of that with no root check. Its only extra powers are two grants (ask the owner, host exec) and one subscription (engine notices).
8. **Self-hosting comes before the cutover.** v2 first runs its own development in a trial room beside v1. Until the cutover, v2 uses its own data, runtime and SSH control folders (`dyna-next`), so its store, owner socket, box sockets and SSH masters never meet v1's. The cutover follows only after the trial works and a dry-run import passes (section 11).
9. **Some v1 features wait until after the cutover.** Voice, workflows, web search through Dyna, computers and the librarian's updates come back after the cutover. The desktop window is ported in M3, so the owner keeps it. Section 9 lists every feature. There is no rollback, so the owner should check that list.
10. **Node jobs are not ported.** CI runs in a box. Host-specific work, such as a GPU task, uses host exec or a box on a host with that label.
11. **CLIProxyAPI is vendored and runs inside the engine.** We keep a fork in `next/third_party/cliproxyapi`, as v1 keeps Pi, so that we can extend it. `dyna engine` runs it in-process through its Go SDK, on the control-plane loopback, so v2 stays one binary (5.7). It lands in M1, because the model route needs it.

## 1. Goals and non-goals

### Goals

1. **Agents never load the owner's machine.** Every session runs in a box on a capacity host. The control plane runs no agents, builds or tests.
2. **One contract.** Events, operations, grants, the API and the migration bundle are defined once in `contract/`. The engine, the CLI, the box agent and the web app use the same types. TypeScript is generated.
3. **No secret in a box.** Model calls, git, GitHub writes and host commands go through the engine. The engine knows the caller by its socket.
4. **Small and legible.** v1 has about 260,000 lines of Go (without tests) in 138 packages. v2 targets under 60,000 lines at the cutover, in nine top-level packages, with no compatibility code.
5. **Fast, honest tests.** The repository holds only fast unit tests. A person or agent checks integration by hand in a sandbox (section 10).
6. **A clean cutover.** One bundle, one commit, no rollback. The store guard protects every other install.
7. **Useful to any owner (r9).** Any repository and toolchain (images from `.dyna/images/`), any Linux host with rootless Podman and SSH, and every model account that CLIProxyAPI supports.

### Non-goals

- No v1 compatibility in `next/`: no v1 store reader, no v1 wire fields, no legacy shims, no rollback path.
- No harness other than Pi. Claude Code is not ported (section 0, decision 4).
- No agent work on the control plane: no host worktrees, host shells, host browsers, room slices or test-slot wrapper.
- No root seat and no attach or detach of outside CLIs.
- No shell hooks. Hooks are typed event subscriptions.
- No JSON state files. JSON stays for settings and config.
- No new web framework. The Svelte app is ported, and each view takes the quiet, dense look of 5.13 as it is ported.
- No shared or untrusted hosts. Boxes run only where only we have admin.
- No integration or end-to-end tests in the repository.
- No box-lite step. M1 goes straight to the full box.

## 2. Layout

```text
next/                    module github.com/resident-labs/dyna (go 1.26), no go.work
  go.mod
  cmd/dyna/              main: the one binary
  contract/              shared types; imports only the standard library
  engine/                the control plane: store, API, approvals, model route, git, timers
  box/                   hosts, images, budgets, boxes, and the box agent
  harness/               the Pi adapter and the fake harness
  room/                  pure room rules: tree, addresses, delivery, reply channels, wake
  migrate/               bundle import (removed after the migration, section 8)
  web/                   the ported Svelte app; types generated from contract/
  docs/                  this spec and the guides
  third_party/cliproxyapi/  vendored CLIProxyAPI fork: its own Go module, our patches, DYNA.md
```

The old module (`dyna-agent`) adds only `cmd/dyna-export`. It imports `next/contract` through a `replace` directive, so the exporter writes the exact bundle types (section 8).

**One binary, three roles.**

| Command | Where it runs | What it does |
|---|---|---|
| `dyna engine` | control plane | serves the API, owns the store, runs CLIProxyAPI in-process and the engine timers |
| `dyna box-agent` | in each box | turn runner, shell server, process guard, transcript stream, model and git routes into the box |
| `dyna <noun> <verb>` | anywhere | the CLI. In a box it uses the box socket. On the control plane it uses the owner socket. |

**Dependency direction.** `contract` imports nothing from the module. `room` imports only `contract`, and it does no I/O: it decides, and the engine acts. `harness` imports only `contract`. `box` imports `contract` and `harness`, because the box agent runs the adapters. `engine` imports `room`, `box`, `harness` and `contract`. `cmd/dyna` wires them. `migrate` imports `contract` and `engine/store`. A unit test enforces this with `go list -deps`.

Sub-packages are the area lead's choice. The expected ones are `engine/store`, `engine/api`, `engine/approve`, `engine/model`, `engine/git`, `engine/sched`, `box/podman`, `box/fakehost`, `harness/pi` and `harness/fakeharness`.

Third-party Go dependencies are allowed only with a reason in the PR. Expected: `modernc.org/sqlite` (pure Go, as in v1), `github.com/spf13/cobra` for the CLI, and the vendored CLIProxyAPI with the modules it needs (5.7). The web app keeps Svelte 5, Vite and strict TypeScript (r7).

## 3. Contract

### 3.1 Rules

- `contract/` holds every type that crosses a process boundary or is stored as a typed body: API calls, events, operations, grants, deliveries and the bundle.
- Times are `time.Time` (RFC 3339 in JSON, Unix milliseconds in the store). Durations are `DurationMS`, an `int64` of milliseconds, so Go, JSON, TypeScript and the store use one unit.
- Closed sets are named string types with constants. No `any`, no `map[string]any`, and no `json.RawMessage` outside the two generated union codecs (r7).
- Values are checked once, where they enter: the API handler or the bundle import. Code further in trusts the types (u3).
- `go generate ./contract` writes `contract/union_gen.go` (the decode switches for event bodies and operation arguments) and `web/src/lib/contract.gen.ts` (all types and a typed API client). CI fails when either file is stale.
- The engine and the box agent can run different releases for a short time (section 5.8). Within v2, API changes are therefore additive: a new field or method is fine, and a removal waits one release. The bundle is the only versioned format.

### 3.2 Identifiers and principals

```go
type (
	RoomID         string // "room_<ulid>"
	SessionID      string // "ses_<ulid>"
	MemberID       string // "mem_<ulid>", an external agent
	BoxID          string // "box_<ulid>"
	EventID        string // "ev_<ulid>"
	OpID           string // "op_<ulid>"
	GrantID        string // "gr_<ulid>"
	TurnID         string // "turn_<ulid>"
	DeviceID       string // "dev_<ulid>", a paired device
	SubscriptionID string // "sub_<ulid>"
	GoalID         string // "g<n>"; v1 numbers are kept at import (M3)
	HostName       string // a registered host, such as "hermeslab"
	RepoName       string // "owner/name" on GitHub
	ProfileName    string // a registered profile, such as "terra"
	DurationMS     int64  // a duration in milliseconds
)

// ControlPlane is the reserved host name of the engine's own machine.
const ControlPlane HostName = "control-plane"

// AllHosts in a grant matches every registered host and the control plane.
const AllHosts HostName = "*"

type PrincipalKind string

const (
	PrincipalOwner   PrincipalKind = "owner"   // the human
	PrincipalSession PrincipalKind = "session" // an agent session; root is one
	PrincipalMember  PrincipalKind = "member"  // an external agent with a pull inbox
	PrincipalEngine  PrincipalKind = "engine"  // the engine as its own sender
)

// Principal is who acts or who receives. The engine sets it from the
// connection, never from a request body.
type Principal struct {
	Kind    PrincipalKind `json:"kind"`
	Session SessionID     `json:"session,omitempty"` // Kind session only
	Member  MemberID      `json:"member,omitempty"`  // Kind member only
}
```

Root is the session of a room that has no parent. The code never compares a name with `root`. Root's powers come from its place at the top of the tree and from two grants (3.5 and 7.2). One policy check says that root holds no goals.

### 3.3 Event

An event is one fact that the engine stored. Posts, turn results, box changes, approvals and notices are all events.

```go
// EventType is "family.verb" in the past tense, such as "post.created".
type EventType string

type Event struct {
	Seq  int64     `json:"seq"`            // total order in the store, set at commit
	ID   EventID   `json:"id"`
	Time time.Time `json:"time"`
	Room RoomID    `json:"room,omitempty"` // empty for machine events
	From Principal `json:"from"`
	Body EventBody `json:"body"`            // the JSON form also carries "type"
}

// EventBody is one registered body struct. The unexported method seals the
// set, so every body is defined in contract/.
type EventBody interface {
	EventType() EventType
	isEventBody()
}
```

Each event type has exactly one body struct. The generator finds them by the `EventBody` interface and writes the decode switch and a TypeScript discriminated union. Wake is not part of an event type. It is decided per recipient at delivery (3.4).

Event types by milestone:

| Milestone | Types |
|---|---|
| M1 | `post.created`, `session.started`, `session.closed`, `turn.started`, `turn.ended`, `box.changed` |
| M2 | `op.requested`, `op.decided`, `op.ran`, `grant.created`, `grant.revoked`, `grant.expired`, `session.moved`, `box.pressure`, `image.built`, `image.failed` |
| M3 | `member.added`, `member.removed`, `goal.*`, `issue.*`, `question.asked`, `question.answered`, `rule.changed`, `pr.opened`, `pr.reviewed`, `pr.merged`, `pr.closed`, `ci.finished`, `repo.main_updated`, `hook.finished`, `checkin.due`, `notice.posted`, `account.quota_low`, `account.failed`, `migrate.handoff` |

`notice.posted` carries a typed `NoticeKind` (such as `host.unreachable` or `account.exhausted`), so every owner-facing notice has a designed card (r8).

The M1 bodies:

```go
type PostCreated struct {
	Text        string       `json:"text"`
	Mentions    []Principal  `json:"mentions,omitempty"`
	Home        Conversation `json:"home"`              // feed or a DM
	ReplyTo     EventID      `json:"replyTo,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type Attachment struct {
	Name   string `json:"name"`
	Media  string `json:"media"`  // MIME type
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"` // the file is stored by hash
}

type SessionStarted struct {
	Session SessionID   `json:"session"`
	Name    string      `json:"name"`
	Parent  SessionID   `json:"parent,omitempty"` // empty for root
	Profile ProfileName `json:"profile"`
	Goal    string      `json:"goal,omitempty"`
}

type SessionClosed struct {
	Session SessionID `json:"session"`
	Archive string    `json:"archive,omitempty"` // refs/dyna/archive/<session>, if any
	Unheld  []GoalID  `json:"unheld,omitempty"`  // M3: open goals left with no holder, because the parent is root (7.4)
}

type TurnStarted struct {
	Session SessionID `json:"session"`
	Turn    TurnID    `json:"turn"`
	Inputs  []EventID `json:"inputs"` // the deliveries this turn takes
}

type TurnOutcome string // "completed" | "failed" | "stopped"

type TurnEnded struct {
	Session SessionID      `json:"session"`
	Turn    TurnID         `json:"turn"`
	Outcome TurnOutcome    `json:"outcome"`
	Final   string         `json:"final,omitempty"`   // the final response
	Replies []ReplyChannel `json:"replies,omitempty"` // where Final was delivered
	Usage   Usage          `json:"usage"`
	Error   string         `json:"error,omitempty"`   // failed only
}

type Usage struct {
	Model            string `json:"model"`
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	CacheReadTokens  int64  `json:"cacheReadTokens"`
	CacheWriteTokens int64  `json:"cacheWriteTokens"`
}

type BoxChanged struct {
	Box     BoxID     `json:"box"`
	Session SessionID `json:"session"`
	Host    HostName  `json:"host,omitempty"`
	From    BoxState  `json:"from"`
	To      BoxState  `json:"to"`
	Reason  string    `json:"reason,omitempty"`
}
```

### 3.4 Delivery and reply channels

The engine stores an event once. It then writes one delivery per recipient. A delivery says where the input came from and where the answer goes (D-579). The `room` package computes both in one function, from facts the engine already has.

```go
type Surface string

const (
	SurfaceFeed    Surface = "feed"    // a post in the room feed, by anyone
	SurfaceDM      Surface = "dm"      // the owner, in this session's DM
	SurfaceCard    Surface = "card"    // the owner, through an HTML card
	SurfaceSession Surface = "session" // the engine, from another session's turn: a brief or a result
	SurfaceService Surface = "service" // a reviewer or helper service (M3)
	SurfaceEngine  Surface = "engine"  // a system event
)

type ConversationKind string // "feed" | "dm"

type Conversation struct {
	Kind    ConversationKind `json:"kind"`
	Session SessionID        `json:"session,omitempty"` // dm only
}

type ReplyKind string

const (
	ReplyFinal ReplyKind = "final" // the engine delivers the final response to Target
	ReplyPost  ReplyKind = "post"  // answer with dyna post, only if needed
	ReplyNone  ReplyKind = "none"  // nothing is owed; end with one line
)

type ReplyChannel struct {
	Kind    ReplyKind    `json:"kind"`
	Target  Principal    `json:"target,omitempty"`
	Home    Conversation `json:"home,omitempty"`
	ReplyTo EventID      `json:"replyTo,omitempty"`
}

type Delivery struct {
	Seq     int64        `json:"seq"`  // the event
	To      Principal    `json:"to"`   // a session or a member
	Surface Surface      `json:"surface"`
	Reply   ReplyChannel `json:"reply"`
	Wake    bool         `json:"wake"` // starts or steers a turn; members never wake
}
```

The mapping from sender and surface to the reply channel is in section 7. Voice adds `SurfaceVoice` when voice is ported (section 9).

### 3.5 Operation and grant

Every agent write that needs a decision is an operation (D-581). Agents call one `Submit`. The engine's registry gives each kind its class, card, check and executor. The contract holds only the data.

```go
// OpKind is "family.verb", such as "hostexec.run" or "repo.settings.set".
type OpKind string

type Class string

const (
	ClassAuto  Class = "auto"  // runs at once
	ClassAgent Class = "agent" // a session above the caller, or the owner, approves
	ClassOwner Class = "owner" // only the owner approves
)

type OpState string // "pending" | "approved" | "denied" | "expired" | "withdrawn" | "done" | "failed"

type Operation struct {
	ID       OpID      `json:"id"`
	Kind     OpKind    `json:"kind"`
	Room     RoomID    `json:"room"`
	By       Principal `json:"by"`
	Class    Class     `json:"class"`
	Card     Card      `json:"card"`
	Args     OpArgs    `json:"args"` // sealed union, one struct per kind
	State    OpState   `json:"state"`
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
	Decision *Decision `json:"decision,omitempty"`
	Result   *OpResult `json:"result,omitempty"`
}

type OpArgs interface {
	OpKind() OpKind
	isOpArgs()
}

type TargetKind string // "repo" | "room" | "host" | "session" | "account"
type ChangeField string // declared per operation kind

type Card struct {
	Title   string   `json:"title"`
	Body    string   `json:"body,omitempty"`
	Target  Target   `json:"target"`
	Changes []Change `json:"changes,omitempty"`
	Command []string `json:"command,omitempty"` // argv, for host exec
}

type Target struct {
	Kind TargetKind `json:"kind"`
	Name string     `json:"name"`
}

type Change struct {
	Field ChangeField `json:"field"`
	Old   string      `json:"old"`
	New   string      `json:"new"`
}

type Decision struct {
	By       Principal `json:"by"`
	Approved bool      `json:"approved"`
	Reason   string    `json:"reason,omitempty"`
	At       time.Time `json:"at"`
}

type OpResult struct {
	ExitCode   int    `json:"exitCode"`
	Error      string `json:"error,omitempty"`
	Output     string `json:"output,omitempty"`     // up to 64 KiB inline
	OutputFile string `json:"outputFile,omitempty"` // the rest, by hash
}
```

Decision rules (engine/approve):

1. The owner's own operations run at once, at every class.
2. `auto` runs at once.
3. `agent` runs at once for a session with no parent (root). Otherwise it waits until any session above the caller in the tree, or the owner, approves. A member's `agent` operation waits for the room's root or the owner. A member with the `approve` grant may approve `agent` operations in its own room only, never `owner` class.
4. `owner` waits for the owner, also for root.
5. The check runs again just before the executor. A card whose `Old` values no longer match fails with `stale`, and nothing runs.
6. A pending operation expires after 24 hours (setting `approvals.expiry`). The same caller, kind and arguments while one is pending returns the pending one.
7. Every operation emits `op.requested` when it waits, `op.decided` when someone decides, and `op.ran` with the result.

A grant gives a principal a power that the tree does not give.

```go
type GrantKind string

const (
	GrantHostExec GrantKind = "hostexec"  // run commands on hosts (section 6)
	GrantAskOwner GrantKind = "ask-owner" // open owner questions and terminal requests
	GrantRead     GrantKind = "read"      // members: read the room
	GrantPost     GrantKind = "post"      // members: post in the room
	GrantApprove  GrantKind = "approve"   // members: approve agent-class operations
)

type GrantMode string // "approve" | "preapproved" (host exec only)

type Grant struct {
	ID       GrantID       `json:"id"`
	Kind     GrantKind     `json:"kind"`
	To       Principal     `json:"to"`
	Room     RoomID        `json:"room"`
	Hosts    []HostName    `json:"hosts,omitempty"`    // host exec: names, or AllHosts
	Commands []CommandRule `json:"commands,omitempty"` // host exec
	Mode     GrantMode     `json:"mode,omitempty"`     // host exec
	Expires  time.Time     `json:"expires,omitzero"`   // zero: no expiry
	By       Principal     `json:"by"`
	Created  time.Time     `json:"created"`
	Revoked  time.Time     `json:"revoked,omitzero"`
}

type CommandRule struct {
	Program string   `json:"program"`        // exact name or path; "*" matches any program
	Args    []string `json:"args,omitempty"` // exact leading arguments
	Prefix  bool     `json:"prefix"`         // more arguments may follow
}
```

Sessions need no grant to work in their own room: the tree gives them that (section 7.2). When the engine creates a room, it creates root and gives it two grants: `ask-owner`, and `hostexec` on `AllHosts` with `Program: "*"`, mode `preapproved` and no expiry. It also adds the default subscription that wakes root on engine notices (8.5).

### 3.6 Bundle

The bundle is the only bridge from v1 (section 8). It is a folder:

```text
global.json          GlobalBundle: version, source, global records, counts
rooms/<name>.json    RoomBundle, one per room, with its own counts
archive/             read-only files; never imported
```

```go
type BundleVersion int

const BundleVersion1 BundleVersion = 1

type BundleSource struct {
	Commit       string `json:"commit"`       // the v1-final commit
	Host         string `json:"host"`
	BackupFile   string `json:"backupFile"`   // the final v1 backup
	BackupSHA256 string `json:"backupSha256"`
}
```

`GlobalBundle`, `RoomBundle`, `BundleCounts` and each part are in section 8.1. The import refuses an unknown version, a missing file, an unknown field, or a count that does not match.

### 3.7 API

- **Transport.** HTTP/1.1 on three kinds of listener: the owner socket (a Unix socket on the control plane, mode 0600), one Unix socket per box (section 5.7), and TCP for the web app (loopback, and paired devices through remote access in M3).
- **Identity.** The listener decides the caller: the owner socket is the owner, a box socket is its session, a device session is the owner with the device's scopes, and a member key is that member (section 7). A request body never names its own caller.
- **Calls.** `POST /v1/<method>` with a contract request struct. The response is the contract response struct, or `Error{Code, Message}`. Each method is a `Method` constant with its request and response types, so the generated TypeScript client is typed.
- **Events.** `GET /v1/events?room=<id>&after=<seq>` streams `Event` values as server-sent events. The web app and `dyna room log --follow` use it.
- **Box channel.** The box agent holds `GET /v1/box/channel` open. The engine sends `TurnStart`, `TurnSteer`, `TurnStop` and `Upgrade` on it. The box agent posts turn output and transcript chunks back with `POST` calls.
- **Box routes.** Only box sockets serve `/model/` (the model route) and `/git/<repo>/` (the git route).

```go
type ErrorCode string

const (
	ErrInvalid     ErrorCode = "invalid"
	ErrNotFound    ErrorCode = "not_found"
	ErrForbidden   ErrorCode = "forbidden"
	ErrConflict    ErrorCode = "conflict"
	ErrStale       ErrorCode = "stale"
	ErrUnavailable ErrorCode = "unavailable"
	ErrStoreFromV1 ErrorCode = "store_from_v1" // the upgrade guard (section 8)
	ErrStoreTooOld ErrorCode = "store_too_old" // older than the schema floor
)

type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}
```

M1 methods: `room.create`, `session.start`, `session.close`, `post`, `events.list`, `box.hello`, `turn.output`, `transcript.append`. Each later milestone adds its methods in the same way.

## 4. Store

### 4.1 Rules

- One SQLite file, `store.db`, in the data folder. The name differs from v1's `dyna.db`, so v2 never opens a v1 file by mistake. WAL mode, `synchronous=NORMAL`, `foreign_keys=ON`, `auto_vacuum=INCREMENTAL` from creation (D-582).
- **Declarative schema.** `engine/store/schema.sql` is the full, current schema. A fresh store runs it. An existing store runs the numbered steps in `engine/store/steps.go` from its `schema_version` up. After the steps, the engine compares the live schema with `schema.sql` and refuses to start if they differ. A unit test proves that the steps from the floor version give exactly `schema.sql`.
- **The floor.** `steps.go` starts at a floor version. Steps below it are deleted once every known store has passed them (rules r17 and r18). A store below the floor gets `store_too_old` with the commit to update through.
- **The upgrade guard.** Before it opens anything, the engine checks the data folder. If `dyna.db` exists and `store.db` does not, it stops with `store_from_v1` and the text in section 8.4, which names the export step. The engine never opens or changes a v1 file. A `store.db` without `meta.format = 'dyna-v2'` is refused too.
- **One writer.** One connection writes, in one goroutine. Every write is one transaction. A state change and its events are written in the same transaction. After `COMMIT`, the writer publishes the committed events to subscribers in `seq` order. Reads use a pool of read-only connections.
- **Checkpoints off the writer path (D-582).** The writer connection sets `wal_autocheckpoint=0`. A checkpointer goroutine with its own connection runs `PRAGMA wal_checkpoint(PASSIVE)` when the WAL passes 64 MiB or 30 seconds pass, and `TRUNCATE` when the writer is idle. A `COMMIT` never waits for a checkpoint.
- **No JSON state.** JSON appears only as the typed body columns (`events.body`, `operations.args`, `operations.card`), which only `contract` decodes. Settings, profiles and harness config stay JSON config files (r1).
- **Files.** Transcripts are append-only JSONL files per session. Attachments and large outputs are files stored by SHA-256. Rows hold the hash and the size.
- **Backups.** Each day the engine runs `VACUUM INTO` from a read connection to `backups/`, and keeps 3. Daily backups are for disasters only. Relocation never uses them; it takes a fresh snapshot after writes stop (section 5.10).
- **Budget.** `store.db` stays under 1 GiB. An archiver moves events older than 180 days for closed sessions to monthly JSONL files under `archive/`. It lands after the cutover, when the growth rate is measured.

### 4.2 Tables

Column types follow SQLite. Times are INTEGER Unix milliseconds (`_at`). IDs are TEXT with their prefix. The `to_kind, to_id` pairs store a `Principal`.

| Milestone | Table | Columns (key first) |
|---|---|---|
| M1 | `meta` | `key` PK, `value` (`format`, `schema_version`, `store_id`, `created_commit`) |
| M1 | `rooms` | `id` PK, `name` UNIQUE, `created_at`, `cpus`, `mem_bytes` (room limit), `paused_at` |
| M1 | `sessions` | `id` PK, `room`, `name`, `parent` NULL, `profile`, `state` (open, dormant, closed), `goal`, `created_at`, `closed_at`; UNIQUE (`room`, `name`) WHERE `closed_at` IS NULL |
| M1 | `events` | `seq` INTEGER PK, `id` UNIQUE, `at`, `room`, `type`, `from_kind`, `from_id`, `body` BLOB; indexes (`room`, `seq`) and (`type`, `seq`) |
| M1 | `deliveries` | (`to_kind`, `to_id`, `seq`) PK, `surface`, `reply_kind`, `reply_target_kind`, `reply_target_id`, `reply_home`, `reply_to`, `wake`, `state` (pending, taken, read), `turn` |
| M1 | `turns` | `id` PK, `session`, `started_at`, `ended_at`, `outcome`, `model`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens` |
| M1 | `hosts`, `host_labels` | section 5.1 |
| M1 | `boxes` | section 5.3 |
| M1 | `transcripts` | (`session`, `path`) PK, `acked_offset`, `file` |
| M2 | `images` | section 5.2 |
| M2 | `operations` | `id` PK, `kind`, `room`, `by_kind`, `by_id`, `class`, `state`, `card` BLOB, `args` BLOB, `args_sha256`, `created_at`, `expires_at`, `decided_by_kind`, `decided_by_id`, `decided_at`, `reason`, `exit_code`, `output_file` |
| M2 | `grants` | `id` PK, `kind`, `room`, `to_kind`, `to_id`, `mode`, `expires_at`, `by_kind`, `by_id`, `created_at`, `revoked_at` |
| M2 | `grant_hosts`, `grant_commands`, `grant_command_args` | section 6.1; no JSON columns |
| M2 | `hostexec_runs` | section 6.3 |
| M2 | `subscriptions`, `subscription_events`, `subscription_args` | section 8.5; the default notice subscription needs them |
| M3 | `archives` | section 5.9 |
| M3 | `room_repos`, `repos`, `repo_attribution`, `members`, `devices`, `device_scopes` | section 7 |
| M3 | `imports` | section 8.5 |
| M3 | the imported records | `rules`, `goals`, `goal_items`, `issues`, `questions`, `prs`, `library_entries`, `accounts` (labels and limits only), `counters`. Each area owner writes the columns in its M3 plan. Profiles, harness config and skills stay config files. |
| M3 | `proxy_auths` | `id` PK, `provider`, `record` (the proxy's auth record, which holds tokens), `updated_at`. Only the embedded proxy's token store reads and writes it (5.7). |

`counters` holds keyed counters (D-580), such as the next issue number per room. A counter update is one primary-key write with prebuilt SQL.

## 5. Machines and box lifecycle

Drafted by dyna:node-shell.

### 5.1 Control plane and hosts

The control plane runs no boxes. It opens one SSH master to each host and does all host work over it. v2 keeps the v1 design from #1250: one Dyna-owned `ControlMaster` per host with `ControlPersist`, and framed replies. Input starts with a `DYNA-IN <bytes>` header, and output ends with a fixed-width `DYNA-END <bytes> <status>` trailer. A reply without a valid trailer is a transport failure, never a success.

The owner adds a host with `dyna host add <name> --ssh user@addr`. The engine then runs the **host check** over the master. A host is `ready` only when every item passes:

- cgroup v2, with `Delegate=yes` for the user manager and the cpu, memory, io and pids controllers delegated;
- rootless Podman 5 or later, with the systemd cgroup manager;
- sshd allows `StreamLocalForwarding`;
- a reflink file system (btrfs or XFS) under the host data folder, or the host gets the `noReflink` flag and copies instead;
- free disk and memory above the host floor (default 20 GiB and 4 GiB);
- the owner states at registration that only they have admin rights on the host.

```go
type HostState string // "checking" | "ready" | "draining" | "unreachable" | "removed"

type Host struct {
	Name      contract.HostName
	SSH       string // user@addr
	State     HostState
	CPUs      int   // schedulable, after the host reserve
	MemBytes  int64 // schedulable, after the host reserve
	DiskBytes int64 // free in the host data folder at the last check
	NoReflink bool
	Labels    []string // owner labels, such as "gpu"
	CheckedAt time.Time
}
```

**Admission** is the only placement code. A box's request is its size, and its limit is its `memory.max`. A box goes on a `ready` host only when the sum of requests on that host, plus this one, fits in `CPUs` and `MemBytes`, and free disk stays above the floor. Otherwise the box waits in `pending`, and `box.changed` names the reason. Only running boxes count. A stopped (dormant) box counts only its disk.

Tables: `hosts` (`name` PK, `ssh`, `state`, `cpus`, `mem_bytes`, `disk_bytes`, `no_reflink`, `checked_at`) and `host_labels` (`host`, `label`).

### 5.2 Images

- **`dyna-base`**: a small Debian image with git, CA certificates, tini, a non-root user, the usual shell tools, Node, and Pi from Dyna's fork (`next/third_party/pi`) at a pinned build. The fork's sign-in commands go unused, because Pi holds no credential in v2.
- **The `dyna` binary is not in the image.** Each host keeps the binaries in `<hostdata>/bin/<version>/dyna`, with a `current` link. Every box mounts `<hostdata>/bin` read-only at `/opt/dyna/bin`. An engine release rebuilds no image.
- **Toolchain layers** live in each repository at `.dyna/images/<name>/Containerfile` and start `FROM dyna-base`. A repository without that folder uses `dyna-base`.
- **Tag**: `dyna-img-<name>:<hash>`, where the hash is SHA-256 over the base digest, the Containerfile and the sorted context files with their contents. The same inputs give the same tag on every host.
- **Builds** run over the master, one at a time per host: at registration, when the default branch changes a file under `.dyna/images/`, and before the first box that needs a missing tag. Each build emits `image.built` or `image.failed`. No registry is needed while there are at most two hosts.

Table: `images` ((`host`, `tag`) PK, `repo`, `name`, `state`, `built_at`).

### 5.3 Box states

```go
type BoxState string

const (
	BoxPending  BoxState = "pending"  // waiting for a host or an image
	BoxCreating BoxState = "creating" // host chosen; volume, repository copy, slice
	BoxStarting BoxState = "starting" // container started; waiting for the box agent's hello
	BoxReady    BoxState = "ready"    // the box agent is connected
	BoxStopped  BoxState = "stopped"  // the session is dormant; volume and slice kept
	BoxClosing  BoxState = "closing"  // archive running, children closing
	BoxDeleted  BoxState = "deleted"  // container, volume and slice removed (end)
	BoxFailed   BoxState = "failed"   // a step failed; Reason says which (end)
	BoxLost     BoxState = "lost"     // host unreachable; may return to ready
)

type BoxSize struct {
	Name     string // "small" | "medium" | "large"
	CPUs     int    // cpu.max in whole CPUs
	MemBytes int64  // memory.high; memory.max is 1.5 times this
	Pids     int    // pids.max
}

type Box struct {
	ID           contract.BoxID
	Session      contract.SessionID
	BudgetParent contract.BoxID // the box whose slice holds this one; empty for root and leads (5.5)
	Host         contract.HostName
	Image        string // dyna-img-<name>:<hash>
	Size         BoxSize
	State        BoxState
	Reason       string // pending, failed and lost only
	Slice        string // the systemd slice of this box's budget
	Updated      time.Time
}
```

| From | To | Cause |
|---|---|---|
| pending | creating | admission placed it, and the image exists |
| creating | starting | volume, repository copy and slice are ready, and `podman run` succeeded |
| starting | ready | the box agent sent `hello` on the box socket within 60 s |
| ready | stopped | the session went dormant: no running turn, shell job or wake |
| stopped | starting | a delivery wakes the session, and admission fits it again |
| stopped | pending | a delivery wakes the session, but the host has no room now |
| pending | starting | admission fits a box that already has its container |
| ready, stopped | closing | the session closes, or its parent closes |
| closing | deleted | archive pushed or not needed, children deleted, container removed |
| creating, starting | failed | a step failed, or `hello` timed out |
| ready, stopped, closing | lost | the host is unreachable (5.10) |
| lost | ready | the host is back, the container still runs, and the box agent reconnected |
| lost | failed | the host is back without the container, or the owner removes the host |

A close while the box is `lost` waits in `closing` until the host returns or the owner removes the host. A box that ends `failed` this way keeps its last checkpoint ref as its archive.

`BoxState` and `BoxSize` live in `contract`, because events and the API carry them. `Host` and `Box` are engine records.

The engine refuses every other transition. Each transition emits one `box.changed` event. A failed box keeps its session open. The operation `box.restart` (class agent) starts a new box from the last checkpoint.

Table: `boxes` (`id` PK, `session` UNIQUE, `parent`, `host`, `image`, `size`, `state`, `reason`, `slice`, `created_at`, `updated_at`; index (`host`, `state`)).

### 5.4 Sizes and growth

| Size | CPUs | memory.high | memory.max | pids.max |
|---|---|---|---|---|
| small | 2 | 4 GiB | 6 GiB | 2048 |
| medium | 4 | 8 GiB | 12 GiB | 4096 |
| large | 8 | 16 GiB | 24 GiB | 8192 |

- The limits apply to the budget slice, which holds the box and all its descendants. A short peak slows the tree down instead of killing a harness.
- Defaults: root `small`, a lead `medium`, a child the size its parent picks, up to the parent's own size. A repository can set a default in `.dyna/images/<name>/size`.
- **Growth.** The box agent reports `box.pressure` when `memory.events` shows `high` throttling for 60 s, or when a process reaches `max`. The session can then submit `box.grow{Box, To}`. Growth within the parent's remaining budget is `auto`. Beyond that it is `agent`, and the parent approves. Growth of root's box is `owner`. Growth also needs admission. When the host cannot fit it, the operation fails with `host-full`, and the box keeps its size.

### 5.5 Budgets and placement

Budget slices follow the tree with systemd's naming, where each dash adds a level: a lead is `dyna-<l>.slice`, its child `dyna-<l>-<c>.slice`, a grandchild `dyna-<l>-<c>-<g>.slice`. Each part is 12 hex characters from the box ID, and admission checks that it is unique on the host. A unit name has at most 255 characters, so this allows about 18 levels. A child's box starts with `podman run --cgroup-parent=<child slice>`, so its use counts against every ancestor below the lead.

Every budget sits under the host slice `dyna.slice`, whose `memory.max` is the host's `MemBytes`. Admission counts sizes, but budgets may grow to 1.5 times their size. The host limit keeps any memory kill inside Dyna's boxes, away from sshd and the master.

A cgroup nests only inside one kernel, so a child runs on its parent's host. Therefore:

- **Root's box** is small and runs on any host.
- **Each lead** (a direct child of root) is a placement root. It gets a top-level slice on the host with the most free memory. The engine counts the requests of root and all leads against the room limit (`rooms.cpus`, `rooms.mem_bytes`). The room still has one budget, enforced by accounting at the top and by cgroups below each lead.
- **A lead's subtree never splits across hosts.** A child that does not fit on its lead's host waits in `pending`.
- **M1** uses one host, hermeslab.

### 5.6 Repository copy and shared caches (M3)

Repositories, git through the engine, checkpoints and the archive on close (5.6 to 5.9) land in M3. In M1 and M2, a box has no repository, so close has nothing to archive.

Each host keeps one bare cache per repository at `<hostdata>/git/<owner>/<repo>.git`. **Hosts never talk to GitHub and hold no GitHub credentials.** The control plane fetches from GitHub and pushes into the host cache over the master, before it creates a box and when it sees the default branch move.

A box's repository is made in the box volume:

1. `git clone --no-checkout --reference <cache> <cache> /work/<repo>`. The cache is mounted read-only at the same path, so the alternates resolve inside the box.
2. `origin` is set to `dyna::<repo>` (5.7).
3. The session branch is checked out: an existing branch from the bundle or the parent, or a new `dyna/<session>` from the base.

New objects go into the box volume, so a box cannot change the cache. Shared dependency caches are per host and per image hash, mounted read-write: `GOMODCACHE`, `GOCACHE`, the pnpm store and the cargo registry. `node_modules` is seeded as a reflink copy when the lockfile hash matches. One box can spoil a shared cache for the others; this is an accepted risk on hosts that only we use.

### 5.7 Box socket, model route and git

**Box socket.** The engine listens on one Unix socket per box, `<runtime>/boxes/<box>.sock`. Over the host's master it adds `ssh -O forward -R <hostdata>/sock/<box>.sock:<runtime>/boxes/<box>.sock` (StreamLocalForward, with `StreamLocalBindUnlink=yes`). The host path is bind-mounted into the box as `/run/dyna/engine.sock`, and no other socket is mounted. The listener maps to one box, the box to one session, and the session to its Principal. No token is sent.

If a master dies, the engine starts it again and adds every forward again. The box agent retries with backoff (0.5 s, doubling to 10 s) and buffers outgoing data up to 64 MiB, so a short loss never ends a turn.

**Model route.** The box agent listens on `127.0.0.1:7781` inside the box. It starts Pi with a generated `models.json` whose providers all point there, with the API key `box`, a placeholder that is not a secret. The `anthropic` provider uses the origin `http://127.0.0.1:7781` with no `/v1`. ChatGPT uses an `openai-responses` provider, and the OpenAI-compatible upstreams use an `openai-completions` provider, both at `http://127.0.0.1:7781/v1` (M0b, M0c). The box agent forwards each request unchanged over the box socket to `/model/`. The engine checks that the session's profile allows the model, replaces the placeholder with the CLIProxyAPI client key, and streams to CLIProxyAPI on the control-plane loopback. Server-sent events pass through without buffering.

Only one layer retries a model call: Pi. The model route never retries, and CLIProxyAPI runs with `request-retry: 0`. The route adds the session ID as CLIProxyAPI's session-affinity key, so a session stays on one account and keeps its prompt cache. The engine pins the CLIProxyAPI release. Its management API listens only on the control-plane loopback, and only the engine holds its management key (5.12).

**Cloaking.** For Claude sign-ins, CLIProxyAPI presents Pi's requests as Claude Code (M0c). It replaces Pi's first system block with Claude Code's identity and billing blocks and keeps Pi's prompt as a later system message. It renames tools to stable `mcp__` aliases and maps the calls back, adds a `metadata.user_id`, and may add a fallback model. `turn.ended` therefore records the model that actually served the turn. Strict cloak mode drops the caller's system prompt, so the engine never turns it on.

**Vendored CLIProxyAPI (M1).** CLIProxyAPI is MIT-licensed Go. Its fork lives in `next/third_party/cliproxyapi`:

- It is the upstream module `github.com/router-for-me/CLIProxyAPI/v8`, pinned to an upstream tag (v8.0.48 at the start), and brought in with `git subtree --squash`. `next/go.mod` points at it with a `replace` directive, and its `go` line follows the fork's.
- `DYNA.md` in that folder records the upstream tag and commit, the sync command, and one entry per patch with its reason. Each patch is its own commit, as in v1's `third_party/pi/DYNA.md`.
- The engine embeds it through the SDK packages only (`sdk/cliproxy`, `sdk/config`, `sdk/auth`): `cliproxy.NewBuilder().WithConfig(cfg).Build()`, then `Run` under the engine's context and `Shutdown` when the engine stops. The engine builds the config in code: listen on `127.0.0.1` only, `request-retry: 0`, strict cloak off, and the management API mounted only with the engine's key and never for remote access.
- In M1 the proxy keeps its sign-ins in its own file store, `<data>/cliproxy/auth` (mode 0700). In M3 the engine registers a token store on `store.db` through the SDK's `Store` interface (`List`, `Save`, `Delete`), so no JSON state is left. `store.db` and its backups then hold tokens and stay 0600.
- Accepted costs: the binary grows by the proxy's dependencies (gin, a Redis client, zeroconf and others), which later patches may trim. A panic in a proxy goroutine stops the engine; systemd restarts it, and box agents buffer meanwhile (5.7).

**Git (M3).** `git-remote-dyna` (a link to `dyna box-agent git-remote`) carries smart HTTP over the box socket to `/git/<repo>/`. The engine checks the session's repositories and proxies to GitHub with its own token. Pushes are allowed only to branches the session owns (`dyna/<session>`, or a branch registered to it) and to `refs/dyna/archive/<session>` and `refs/dyna/checkpoint/<session>`. Fetches are allowed for the room's repositories.

**Network.** Boxes have outbound internet for package installs, so they may reach the desktop on the LAN or through the tunnel. The engine's network listeners therefore accept only paired devices, and boxes hold no device keys. The box socket is a box's only way into the engine.

### 5.8 Box agent (M1; checkpoint in M3)

`dyna box-agent` runs under tini as the only child of PID 1. It has four parts:

- **Turn runner.** It drives one Pi process per box over RPC. It takes `TurnStart`, starts or resumes Pi, and posts the typed turn result. Pi's own queues hold the input; the box agent keeps none:
  - A delivery that arrives during a turn goes to Pi's steer queue, or to its follow-up queue when its wake rule waits for the turn end.
  - Each input carries its delivery ID. A delivery is done only when Pi's session file shows the input. After a reconnect, the engine resends the deliveries that are not done, and the turn runner skips an ID that the session file already holds.
  - Pi compacts by itself, also after a context-overflow error. The box agent adds no compaction logic. Pi loads only the extensions that the image lists.
- **Shell server.** The MCP shell for the harness. Commands run locally in the box, with no `node=` option and no SSH hint (D-585).
- **Process guard.** Each shell job runs in its own child cgroup. Stopping a job kills its cgroup. At turn end, the guard stops jobs that the harness did not ask to keep.
- **Transcript stream.** It tails the harness transcript and posts append-only chunks `{Path, Offset, Bytes}`. The engine acknowledges each offset and keeps the copy. After a reconnect, the box agent resends from the last acknowledged offset.

**Checkpoint (M3).** After each turn, the box agent commits the working tree, with untracked files that are not ignored, through a temporary index and `commit-tree` (the v1 #1246 method). It pushes the commit to `refs/dyna/checkpoint/<session>`, and skips the push when nothing changed. The session branch and the index do not change.

**Release skew.** `hello` carries the binary version. When it differs from the engine's, the engine sends `Upgrade` once the box is idle, and the box agent runs `/opt/dyna/bin/current/dyna box-agent` in its place.

### 5.9 Close (archive in M3)

Close ends the whole subtree, as the card decided: each child is closed and deleted, deepest first, with its own archive. To keep a child running, move it to another parent first (7.4). Then, for the box itself:

1. Stop the harness and every shell job.
2. Archive each repository. Push HEAD and a snapshot of the dirty tree (the checkpoint method) to `refs/dyna/archive/<session>`. The ref points at the snapshot, whose first parent is HEAD. Skip this when the tree is clean and HEAD is already on the remote.
3. Delete the checkpoint ref.
4. `podman rm`, delete the volume, stop the slice, and remove the forward and the listener.
5. Emit `box.changed` to `deleted`, then `session.closed`.

The engine deletes archive refs after 14 days. `dyna session restore <session>` starts a new box from the archive. If an archive push fails, the box stays in `closing` with a reason. A box is never deleted without its archive.

Table: `archives` (`session` PK, `repo`, `ref`, `commit_sha`, `created_at`, `expires_at`).

### 5.10 Host failure and relocation

**Host failure.** The engine sends a heartbeat over each master every 10 s. After 3 misses, the host is `unreachable`, and its boxes are `lost`. When the host returns, the engine reconciles by the container label `dyna.box=<id>`: a running container goes back to `ready` when its box agent reconnects, and a missing one becomes `failed`. `box.restart` starts a new box on any host that fits, from the last checkpoint and the engine's transcript copy. For a `lost` box, it waits until the old container is proven gone: the host returned and reconciled, or the owner removed the host. Otherwise two boxes could push the same branch. In the new box, the turn runner gives each tool call that started without a result the result "interrupted; state unknown", and Pi never runs that call again. A lost host therefore loses at most one unfinished turn. `dyna host drain` stops new placement on a host.

**Control-plane relocation.** Boxes keep running throughout, and their box agents buffer.

1. `dyna engine export-state <dir>` stops the API and the writer, so no write can follow the snapshot. It then runs `PRAGMA wal_checkpoint(TRUNCATE)` and copies `store.db` to `<dir>` with the SQLite backup API. Daily backups are never used here.
2. It copies, in the same stopped state, all file-backed state: the transcripts, the attachment and output files (by hash), the config files, the CLIProxyAPI auth folder until M3 moves it into `store.db` (5.7), and the Connect secrets. It writes a manifest of each file's path, size and SHA-256, plus the store's last event `seq`.
3. The engine on the old host stays stopped. It refuses to start again while the export marker is in its data folder, so two engines never serve the same boxes.
4. `dyna engine import-state <dir>` on the new host checks every hash in the manifest and runs `PRAGMA integrity_check` on the store. It refuses to start on any mismatch.
5. The new engine starts. It opens the masters, adds every forward again, and the box agents reconnect and resend from their last acknowledged offsets.

The new host must reach every host over SSH. The export holds secrets, so it moves only over SSH between our hosts and is deleted after the import.

### 5.11 Carried from D-585

- The shell tool runs locally in the box. `node=` is not built, and the #1243 SSH hint is gone.
- Node jobs are not ported (section 0, decision 10). CI runs in a box. Host-specific work uses host exec, or a box on a host with the right label. The snapshot method from #1246 lives on as the checkpoint and the archive.
- One host has one master, from #1250. It carries image builds, host exec and the box socket forwards.


### 5.12 Accounts page (M3)

The owner manages every model account on one page of the v2 web app. It has the features of cliproxyapi-dashboard, but not its look or code (5.13). The browser never talks to CLIProxyAPI. The engine calls CLIProxyAPI's management API on the control-plane loopback with the management key, which only the engine holds.

- **Providers.** One row per provider and account, with its label, state and room limits. A provider with a sign-in has a "Connect" button, which starts CLIProxyAPI's own OAuth or device-code flow. The page shows the link or code, and the token goes straight to CLIProxyAPI. A provider with a key has a key field. The engine passes the key to CLIProxyAPI and never shows it again; only the proxy's token store keeps it.
- **Quota.** For each account and each provider: the usage against each limit window and the time of the next reset. The engine uses no model credential for this. It has two sources: the management API, and the rate-limit headers on responses that the model route already carries, when CLIProxyAPI passes them (`passthrough-headers`). When neither gives a limit for a provider, the page shows the usage that the proxy counted and marks the limit unknown. Such an account gets no `account.quota_low`, only `account.failed`.
- **Alerts.** The events `account.quota_low` (below 10% of a window, or an owner-set floor) and `account.failed` (sign-in rejected or refresh failed) are typed and carry the account and the reason. Only these two states get a colored chip, through the shared `StatusPill` (r8, r16). Every other value is muted text.
- **Methods.** `accounts.list`, `accounts.connect`, `accounts.set_key`, `accounts.remove` and `accounts.set_limits` are owner-class operations (3.5). The quota view reads `accounts.quota`.

### 5.13 Web design

The v2 web app stays Svelte 5 and is ported view by view. Each ported or new view follows T3 Code's design approach: quiet and dense, with few borders and few badges. T3 Code is a reference for the approach only; no code is copied.

Every PR that adds or changes a view has one required step (owner rule):

- The builder attaches one side-by-side image to the PR. The left half is the new or changed view in the v2 app. The right half is the T3 Code page that is most similar to it. Both use the same window size and theme.
- The reviewer checks the PR against that image and requests changes when the view is louder or less dense than the T3 Code page, or when the image is missing.
- Each UI builder's brief repeats these steps.

dyna:lead-rewrite is the design and taste reviewer for all v2 frontend work (owner rule):

- Before frontend work starts, the builder sends its design, with the T3 Code side-by-side, to dyna:lead-rewrite. Work starts only after that approval.
- dyna:lead-rewrite approves each frontend PR on design and taste. The normal reviewers check correctness and everything else.

## 6. Host exec

Drafted by dyna:node-shell. Host exec runs one command outside every box: on a capacity host or on the control-plane host. Examples: `podman system prune`, a service restart, or reading host logs. It is an operation, and it always needs a grant.

### 6.1 Grants

A host-exec grant is a `contract.Grant` with `Kind: hostexec` (3.5).

- **Matching is by argv**, never by a shell string. `sh -c "..."` matches only a rule whose `Program` is `sh` or `*`. An exact rule refuses extra arguments; a prefix rule accepts them.
- When several grants match, `preapproved` wins over `approve`. When none matches, the call fails with `no-grant`, and nothing runs.
- The class of `hostexec.run` comes from the grant that matched: `auto` when a `preapproved` grant matches, and `owner` when only an `approve` grant matches. A member can never submit it.

Tables: `grant_hosts` (`grant`, `host`), `grant_commands` (`grant`, `idx`, `program`, `prefix`) and `grant_command_args` (`grant`, `idx`, `pos`, `arg`).

### 6.2 Default grant, owner grants, revoke and expiry

- **Root's default grant** is made with the room: every host including the control plane, any command, `preapproved`, no expiry (3.5). The owner can narrow it like any other grant.
- **Owner grants.** Only the owner grants host exec: `dyna hostexec grant` or the web card, as the operation `hostexec.grant` with class `owner`. A session may ask for a grant; the request goes to the owner. The default expiry is 24 hours. `--expires never` must be explicit.
- **Revoke.** `dyna hostexec revoke <grant>`, by the owner. It acts at once: pending runs under the grant are denied, and running commands get SIGTERM, then SIGKILL after 5 s.
- **Expiry** is checked at match time. A sweep marks expired grants and emits `grant.expired`. A running command is not stopped at expiry; only a revoke stops it.
- The web app lists active grants with their expiry and a revoke button.

### 6.3 Run path

1. The agent runs `dyna hostexec run --host <h> [--timeout 10m] -- <argv>` in its box. The request comes over the box socket, so the engine knows the principal.
2. The engine matches the grants and submits `hostexec.run{Host, Argv, Cwd, Timeout, Grant}`. It runs at once or waits for the owner.
3. On a capacity host, the command runs over the master as `systemd-run --user --scope --unit=dyna-hostexec-<op> -p RuntimeMaxSec=<timeout> -- <argv>`, with framed replies. On the control plane, the same `systemd-run` runs locally.
4. The command gets the host user's login environment. The engine adds no secrets. Stdin is empty.
5. Output streams live to the caller and to watchers in the web app. Up to 64 KiB is kept inline in the result; the rest is a file named in the result. The default timeout is 10 minutes, and the maximum is 2 hours.
6. The caller gets the exit code and the output. A run longer than the shell's foreground window works like a long shell job: the caller wakes on `op.ran`.

Every run emits `op.requested` (when it waits), `op.decided` (when someone decides), and `op.ran` with the exit code. No command runs without these events.

Table: `hostexec_runs` (`op` PK, `grant`, `host`, `scope`, `started_at`, `ended_at`, `output_bytes`, `stopped`), where `stopped` is empty, `timeout` or `revoked`. The exit code and output file stay on the operation.

### 6.4 Control-plane safety and accepted risks

Host exec on the control plane runs as the owner's user. To stop such a command from acting as the owner through the engine, each control-plane run gets its own scope, `dyna-hostexec-<op>.scope`. The owner socket reads each caller's cgroup. If the path contains `dyna-hostexec-`, the engine treats the caller as the grant holder and refuses every owner-only call with `hostexec-scope`.

Accepted risks (owner, 2026-10-30):

- `*` on a host is full access to that host. On the control plane, that includes the CLIProxyAPI sign-ins and the owner's files. Root's default grant relies on root's judgment.
- A command can leave its scope, for example with `systemd-run --user`. The scope check stops mistakes, not a hostile command.
- A capacity-host command runs as the host user. It can read or change every box on that host and the shared caches. It can also connect to any box's forwarded socket under `<hostdata>/sock/` and act as that box's session, with its model route and git push rights. Hosts hold no model or GitHub credentials, and only we have admin on them.
- Output can contain host secrets. Only room members can see it.

## 7. Identity, rooms and the tree

Drafted by dyna:code-architectural.

### 7.1 Principals

The engine derives every caller's `Principal` from the connection (3.7). A caller never states its own identity.

| Kind | Who | How the engine knows it |
|---|---|---|
| `owner` | the human | the owner socket, checked with `SO_PEERCRED` for the owner's UID; or a paired device token |
| `session` | one session, root included | its box socket |
| `member` | an external agent in one room | its member token |
| `engine` | the control plane | internal only; never accepted on a listener |

- **Owner, local.** The owner socket is `$XDG_RUNTIME_DIR/dyna/owner.sock`, mode 0600, on the control plane. No box mounts it. A caller inside a host-exec scope is refused (6.4).
- **Owner, remote.** A paired device sends its token through Connect or the web server. The engine resolves it to the owner plus a `DeviceID`. The device's scopes limit which operation kinds it may submit. Scopes filter the owner; they are not a second identity.
- **Session.** The engine maps each box socket to its box and session when it starts the box. A restarted box gets a new socket, and the old one stops resolving.
- **Engine.** Notices, CI results and merge results come `From` the engine. The engine never posts, files or closes issues as root.

### 7.2 Root is a position, not a role

Root is the session with no parent. Four things replace the 89 root checks in 12 v1 packages:

| What | Form |
|---|---|
| Approve agent-class operations | The tree: any session above the caller may approve (3.5). Root is above every session. |
| Start, move, stop or close sessions | The tree: any session above another may do this. Root is above every session. |
| Ask the owner; run host commands | The grants `ask-owner` and `hostexec`, made with the room (3.5). The owner can narrow or revoke them. |
| Engine notices and unowned repository events | A default room subscription that wakes root (8.5). |
| Hold no goals | One rule: the session with no parent holds no goals. Goals that a close would hand to root stay with no holder (7.4). |

Root has its own prompt template, chosen at room creation. These v1 cases have no v2 form: the dual address (`dyna` and `dyna:root`), the root seat with attach and detach, the service flag on root, `NeedsRoot` wake routing, and the engine acting as root.

### 7.3 External members (D-583)

An external member is a message channel. It has no box, no wake, no quota and no model route.

- **Address**: `@<room>:ext-<name>`. The `ext-` prefix is reserved, so a member never shares a name with a session.
- **Add**: the owner submits `member.add{Room, Name, Grants}`. The engine makes a random 256-bit token and shows it once on the result card. The store keeps only its SHA-256 hash.
- **Credential**: the member's CLI reads `DYNA_MEMBER_TOKEN`, or a 0600 file that `dyna member login` writes. It reaches the engine through the web server, locally or through Connect.
- **Pull inbox**: every delivery to a member is stored. `dyna inbox` lists them, and `dyna inbox ack <seq>` marks them read. The engine never pushes to a member and never starts a process for it.
- **Grants**: `read` by default. The owner may add `post` and `approve`. A member never gets `hostexec`.
- **Remove**: `member.remove` revokes the token at once. Past posts stay.

### 7.4 One tree per room (D-584)

- A room has exactly one root. Every other session has a parent in the same room. No session is a sibling of root.
- A session that the owner starts goes under root, or under the session that the owner picks.
- **Close** closes the session and its whole subtree, deepest first, and deletes their boxes after their archives (5.9). The tracked PRs and archive refs of every closed session go to the closing session's parent. Their open goals go to that parent too, unless the parent is root: root holds no goals (7.2), so those goals stay with no holder, and the `session.closed` event lists them for root to assign, as at import (8.3). Root cannot be closed. It can be restarted in a new box with the same session ID, grants and history.
- **Move**: `session.move{Session, NewParent}` by the owner or a session above both places. The engine refuses a cycle, a move to another room, and a move to a parent on another host. A box's volume holds its repository and its harness state, and the engine never copies volumes between hosts; to change hosts, close the session and start a new one. A child's box sits in its parent's budget slice, so a move re-creates every box in the moved subtree:
  - First the engine holds new wakes for the subtree and tells each box agent in it to refuse new shell jobs. Each agent applies the block and lists its running jobs in the same reply, so no job can start between the check and the block. If any job runs, or an agent does not reply, the engine cancels the move, lifts the hold and the block, and names the job or the box; the mover stops the job or waits. Otherwise the engine waits until every running turn in the subtree ends. Those turns cannot start a job, so the subtree is idle.
  - When the subtree is idle, the engine stops its containers deepest first. It then creates each again from the same volume, the moved session first and its children after it, because a child's slice lives in its parent's. A moved session that becomes a lead gets a top-level slice (5.5).
  - The new box agents start without the block, and the held wakes then run as usual. Each session resumes from the harness conversation in its volume, as after dormancy (5.3), and `session.moved` records the new parent.

### 7.5 Reply channels (D-579)

The engine sets the surface and the reply channel of each delivery from its source (3.4). The sender never sets them. Root uses the same table.

| Source of the input | Surface | Channel | Target and home |
|---|---|---|---|
| Owner post in the feed that mentions the session | feed | final | owner, feed, reply to that post |
| Owner post in the feed with no mention (root gets it) | feed | final | owner, feed, reply to that post |
| Owner post in the session's DM | dm | final | owner, `dm:<session>`. The parent does not wake. |
| Brief from `session start` | session | final | parent |
| Parent post that mentions the session | feed | final | parent, feed |
| Card submit | card | final | owner, feed, reply to the card post |
| Member post that mentions the session | feed | final | member, its inbox and the feed |
| Child result | session | post | the child |
| Sibling post, other-room post | feed | post | the sender |
| Group mention only (`@children`, `@siblings`) | feed | none | none |
| Check-in, engine notice, service event, shell or job end | engine or service | none | none |
| Voice dispatch (after voice is ported) | voice | final | owner, the call; the feed if the call ended |

- **final**: the engine delivers the final response to the target, in the home, as a reply. If the turn already posted to that target, the engine delivers the final response quietly (no wake), with a link to that post.
- **post**: nothing is delivered. The session posts only if the sender must act.
- **none**: the final response stays in the transcript. The frame tells the session to end with one line.

A turn with several inputs owes one reply per distinct `final` target.

### 7.6 Rooms and repositories

- A room lists zero or more repositories. If it lists any, exactly one is its default.
- Each repository has exactly one default room. Other rooms may also list it.
- **Events follow ownership.** An event about something a session owns goes to that session's room and wakes that session: `pr.merged` for its tracked PR, `ci.finished` for its run, or `pr.reviewed`. After close, the owning session is the nearest open ancestor.
- **Unowned events**, such as `repo.main_updated`, an outside PR or a new GitHub issue, give one delivery to each room that lists the repository. Only the root of the repository's default room wakes. The store keeps one event row.
- **Shared settings.** Merge policy, CI mode, reviewer profile, test budget and GitHub attribution belong to the repository. They change only through `repo.settings.set`, class `owner`. This also enforces rule u4.

```go
type RoomRepo struct {
	Repo    RepoName `json:"repo"`
	Default bool     `json:"default"`
}

type MergePolicy string     // "queue" | "manual"
type CIMode string          // "github" | "box"
type AttributionKind string // "commit" | "pr" | "comment"

type AttributionLine struct {
	Kind AttributionKind `json:"kind"`
	Line string          `json:"line"`
}

type RepoSettings struct {
	DefaultRoom     RoomID           `json:"defaultRoom"`
	MergePolicy     MergePolicy      `json:"mergePolicy"`
	CIMode          CIMode           `json:"ciMode"`
	ReviewerProfile ProfileName      `json:"reviewerProfile"`
	TestBudget      DurationMS       `json:"testBudget"`
	Attribution     []AttributionLine `json:"attribution,omitempty"`
}
```

Tables: `room_repos` ((`room`, `repo`) PK, `is_default`), `repos` (`repo` PK, `default_room`, `merge_policy`, `ci_mode`, `reviewer_profile`, `test_budget_ms`), `repo_attribution` (`repo`, `kind`, `line`), `members` (`id` PK, `room`, `name`, `token_sha256`, `created_at`, `removed_at`; UNIQUE (`room`, `name`)), `devices` (`id` PK, `name`, `token_sha256`, `paired_at`, `revoked_at`) and `device_scopes` (`device`, `scope`).

## 8. Migration bundle and cutover

Drafted by dyna:code-architectural.

### 8.1 Bundle parts

The bundle folder has mode 0700. It holds no raw secret, but it holds device token hashes, so it is never posted or attached.

v1 sessions are not imported, so their IDs do not exist in v2. Each imported record therefore carries its author as `ImportedAuthor{Kind, Label}`: the kind is `owner`, `session` or `engine`, and the label is the v1 address as text, such as `dyna:code-architectural`. The store keeps the label as text. Only each room's new root gets a real session ID. An unanswered question whose asker is gone goes to the room's root.

```go
type ImportedAuthor struct {
	Kind  PrincipalKind `json:"kind"`  // owner, session or engine
	Label string        `json:"label"` // the v1 address, for display only
}
```

```go
type GlobalBundle struct {
	Version       BundleVersion  `json:"version"`
	Exported      time.Time      `json:"exported"`
	Source        BundleSource   `json:"source"`
	Rules         []Rule         `json:"rules"`         // global rules
	Settings      SettingsBundle `json:"settings"`      // cleaned settings.json
	Profiles      []ProfileBundle `json:"profiles"`
	Hosts         []HostBundle   `json:"hosts"`         // from machines and local_ci_modes
	Accounts      []AccountLabel `json:"accounts"`      // provider, label, limits; no credential
	Devices       []DeviceBundle `json:"devices"`       // name, scopes, token hash
	Repos         []RepoBundle   `json:"repos"`         // settings, default room, library entries
	Skills        []SkillBundle  `json:"skills"`
	Subscriptions []Subscription `json:"subscriptions"` // machine scope
	Counts        BundleCounts   `json:"counts"`
}

type RoomBundle struct {
	Version       BundleVersion    `json:"version"`
	Name          string           `json:"name"`
	Repos         []RoomRepo       `json:"repos"`
	Limits        RoomLimits       `json:"limits"`
	Color         string           `json:"color"`
	Rules         []Rule           `json:"rules"`
	Goals         []GoalBundle     `json:"goals"`     // active, with items; no holder
	Issues        []IssueBundle    `json:"issues"`    // open and goal-linked; D-numbers kept
	NextIssue     int              `json:"nextIssue"`
	PRs           []TrackedPR      `json:"prs"`       // open only
	Questions     []QuestionBundle `json:"questions"` // unanswered only
	MCPServers    []RoomMCPServer  `json:"mcpServers"`
	IssueWatches  []IssueWatch     `json:"issueWatches"`
	Subscriptions []Subscription   `json:"subscriptions"` // room and session scope
	Handoff       []SessionHandoff `json:"handoff"`       // one card per open v1 session
	Counts        BundleCounts     `json:"counts"`
}

type BundleCounts struct {
	Rules, Goals, GoalItems, Issues, PRs, Questions int
	LibraryEntries, Subscriptions, Hosts, Accounts  int
	Devices, Sessions                               int
}

type SessionHandoff struct {
	Name       string   `json:"name"`
	Profile    string   `json:"profile"`
	Parent     string   `json:"parent"`
	Goals      []string `json:"goals"`
	ArchiveRef string   `json:"archiveRef,omitempty"` // refs/dyna/archive/<session>
	LastPost   string   `json:"lastPost"`             // cut to 2,000 characters
}
```

Goals import with no holder; root assigns them from the handoff card. Sessions, turns, harness conversations, CI results, shell output and jobs are not carried (the inventory's drop list, about 70 GB). The `archive/` folder holds the room feeds, files, transcripts, finished goals and closed issues, with a manifest of paths, sizes and hashes.

### 8.2 How the exporter gets the types

The old `go.mod` requires `github.com/resident-labs/dyna` with `replace github.com/resident-labs/dyna => ./next`, and `cmd/dyna-export` imports `contract` directly. This keeps one definition. It holds under three conditions:

1. `contract` imports only the standard library, so `next/` cannot raise any dependency version in the frozen v1 binary. Old CI compares `go list -m all` with and without the line; only `github.com/resident-labs/dyna` may differ.
2. The exporter is built only from the `v1-final` tag. The cutover commit deletes the old module and the `replace` line with it.
3. Any build step that copies `go.mod` also copies `next/go.mod`.

### 8.3 Cutover steps

The external Opus agent runs steps 3 to 10 from a prompt we write. The owner does steps 1, 2 and 11.

0. **Gate.** M3 passes, and the self-hosting trial works. A terra agent exports from a copy of the latest daily backup and imports in a sandbox box. Every count matches a SQL count on the backup. The owner approves the gate card, which lists every feature in section 9 with its result.
1. **Owner: auto-update off** on every install, including the hourly self-update hook.
2. **Owner: pause.** `dyna pause --all` stops new turns and the merge service. Wait until no turn runs.
3. **Archive trees.** Each open session with a dirty or unmerged worktree pushes a commit to `refs/dyna/archive/<session>` (the #1246 method). These refs are kept 30 days.
4. **Stop v1 and take the final backup** with the SQLite backup API. Copy it to the desktop archive folder and to hermeslab.
5. **Tag `v1-final`** on main. Main holds `cmd/dyna-export` and `next/` at M3.
6. **Export.** Build `cmd/dyna-export` at `v1-final` on hermeslab (r19). Run it on the desktop against the final backup, read-only. `dyna-export verify` compares the counts with SQL counts. Any difference stops the cutover.
7. **Cutover commit.** One commit moves `next/*` to the repository root and deletes the old tree. The owner merges it on GitHub, because the v1 merge service is stopped.
8. **Install v2** from the cutover commit, built on hermeslab. Its store is `~/.local/share/dyna/store.db`.
9. **Import.** `dyna import <bundle>` writes the store in one transaction and records the bundle hash. It copies the Connect secrets (`machine.key`, `jwks.json`, `link.json`, `tunnel-token`) file to file with mode 0600, so they never enter the bundle. It then moves the v1 files to `~/.local/share/dyna-v1/`. The owner deletes that folder after the first good week.
10. **Start.** `dyna engine` starts each room's root in a box. Each root gets one `migrate.handoff` event with its open goals, PRs, questions, issue count and handoff cards.
11. **Owner checks.** The owner checks that the CLIProxyAPI sign-ins from the trial work (the auth folder moves with the engine), opens a room from a paired device, and turns auto-update back on.

### 8.4 Upgrade guard and other installs

Before it opens anything, v2 checks the data folder. If `dyna.db` exists and `store.db` does not, it exits with `store_from_v1` and this text:

> This folder holds a Dyna v1 store (`<path>/dyna.db`). Dyna v2 cannot open it, and there is no way back to v1 after you import. To move your data: (1) stop the v1 engine; (2) build the exporter from tag `v1-final`: `git clone --branch v1-final https://github.com/resident-labs/dyna && cd dyna && go build ./cmd/dyna-export`; (3) run `./dyna-export --out ~/dyna-bundle`; (4) run `dyna import ~/dyna-bundle`. See docs/migrate.md.

- The v2 install script detects a v1 store and runs steps 2 to 4 after one confirmation.
- `dyna import` refuses a bundle whose version it does not know, and a data folder that already has `store.db`.
- **When `migrate/` goes.** It is deleted right after the owner's installs have imported. That commit is tagged `v2-import-last`, and `floor.go` names it (r18). From then on, step 4 of the guard text says to build `dyna` at `v2-import-last` for the import. The guard itself stays.

### 8.5 Hooks as typed subscriptions

```go
type SubscriptionScope string // "machine" | "room" | "session"

type Subscription struct {
	ID      SubscriptionID    `json:"id"` // "sub_<ulid>"
	Scope   SubscriptionScope `json:"scope"`
	Room    RoomID            `json:"room,omitempty"`
	Session SessionID         `json:"session,omitempty"`
	On      []EventType       `json:"on,omitempty"`    // or Every
	Every   DurationMS        `json:"every,omitempty"` // or On
	Wake    *Principal        `json:"wake,omitempty"`  // wake this principal with the event, or
	Run     *RunSpec          `json:"run,omitempty"`   // run a command in a small box
}

type RunSpec struct {
	Command []string  `json:"command"`
	Image   string    `json:"image"`
	Size    string    `json:"size"`            // a box size name
	Files   []RunFile `json:"files,omitempty"` // written into the box before the command
}

type RunFile struct {
	Path string `json:"path"` // inside the box
	Body string `json:"body"`
}
```

Exactly one of `On` and `Every`, and exactly one of `Wake` and `Run`, is set. A `Run` subscription runs in a box, never on a host. Each run ends with a typed `hook.finished` event (r8).

| v1 hooks | Count | v2 form |
|---|---|---|
| Engine timers: doctor, account usage, check-ins, maintenance, harness versions | 5 | engine scheduled tasks; not subscriptions |
| Librarian on main-updated, pr-merged, every 10 min | 7 | not imported, because the librarian comes after the cutover. They are listed on the dyna room's handoff card and made again with the librarian. |
| Owner self-update script | 1 | the built-in updater |
| horizon upstream-watch | 1 | `Run` in a small box in room horizon, every 30 min. The bundle carries the script as a `RunFile`. |
| Notices to root | new | default room subscription: `Wake` root on `notice.posted` and unowned repository events |

Tables: `subscriptions` (`id` PK, `scope`, `room`, `session`, `every_ms`, `wake_kind`, `wake_id`, `run_image`, `run_size`, `created_at`), `subscription_events` (`sub`, `event_type`), `subscription_args` (`sub`, `pos`, `arg`), `subscription_files` (`sub`, `path`, `body`) and `imports` (`bundle_sha256` PK, `version`, `source_commit`, `imported_at`).

### 8.6 Open points

- horizon's root confirms that `upstream-watch` needs no desktop state other than its script. If it does, that state moves into the room or the script changes before the cutover.

## 9. Feature map

Drafted by dyna:code-architectural. Every feature in the v1 library map, with its v2 milestone. The cutover gate needs every M1, M2 and M3 row, because there is no rollback. "Data at M3" means the import keeps the records, but the feature comes after the cutover.

| Feature | Milestone | Reason |
|---|---|---|
| engine, rooms, system-events, session-records, profiles | M1 | the core of the thin slice |
| harnesses, pi, jsonrpc | M1 | the turn runner in the box drives Pi over RPC |
| cli | M1, growing each milestone | `dyna room log` is the M1 check |
| authority | M1 principals; M2 grants | socket identity is the M1 core |
| accounts | M1 one sign-in in CLIProxyAPI; M3 the Accounts page (5.12) with sign-in, keys and quota | model calls need one account at M1 |
| shells | M1 in-box shell server; M2 secret masking | every turn runs commands |
| nodes | M1 hermeslab; M3 Boat and pve | renamed hosts |
| settings | M1 store; M3 import and daily backups | backups are required at the gate |
| approvals | M2 | one Submit |
| sessions, session-dormancy | M1 one session; M2 the tree, children and session dormancy | dormant boxes must stop, or host memory fills |
| web-app, web-server, room-feed, session-view | M2 | the room view, ported |
| files-visuals | M2 cards and files; after: the Chromium visual check | cards carry owner decisions |
| goals, goal-service, parent-checkins | M3 | leads and builders build v2 in v2 |
| questions | M3 | `ask-owner` and card answers |
| prs, reviewer-pool, pr-comment-trust | M3 | self-hosting tracks, reviews and merges PRs; untrusted comments stay data |
| pr-merge-queue | M3, simplified | merging stays with the merge service |
| local-ci | M3 as CI in a box; act dropped | merges need a CI result |
| local-issues, github-issues, room-prs | M3 | 288 open issues keep their D-numbers |
| hooks | M3 | typed subscriptions, imported |
| maintenance | M3 | box, archive ref and store pruning |
| remote-access, connect, device-scopes | M3 | phone access must work on cutover day |
| settings-accounts, settings-devices | M3 | sign in again and pair devices after the cutover |
| skills | M3 | small catalog; fl-rea uses it |
| install | M3 the install script and guard path; after: release pipeline, WSL image, branch QA | other installs need the guard at the cutover |
| updates | M3 manual `dyna update`; after: hourly auto-update | the owner turns updates back on in step 11 |
| desktop | M3 | a thin window over the web app; the owner keeps it at the cutover |
| relay | no change | a separate module and Cloudflare worker with the same protocol |
| library | data at M3; after: the librarian session | the map must survive; updates can wait |
| issue-watch, room-mcp | data at M3; after | no self-hosting step needs them |
| voice and its children | after | no self-hosting step needs voice |
| workflows | after | leads with children cover self-hosting |
| web-search | after | Pi has no built-in search, so agents have no web search until this port (9.1) |
| computers, computer-reminders | after | no self-hosting step needs a desktop in a box |
| command-palette, library-page, review-workspace, services-page, settings-mcp, settings-profiles, web-settings | after | the CLI covers them until they are ported |
| ci-test-budget, pr-conflict-radar, idle-compaction | after | conveniences; Pi compacts by itself |
| session-transfer | dropped | one harness, so no session format to convert |
| jobs | dropped | CI runs in a box; host-specific work uses host exec or a box on a labeled host |
| worktrees | dropped | the box volume is the worktree; close archives to a ref; each box has its own ports |
| load-doctor | dropped | budgets with `memory.high` and `memory.max` replace it |
| test-slots | dropped | box budgets bound concurrent tests |
| settings-harnesses | dropped | harness versions live in the image; sign-in moves to accounts |
| claude-code | dropped | Pi is the only harness (section 0, decision 4) |

**What the owner gives up from the cutover until each port:** voice calls, workflows, web search through Dyna, computers (browser agents), the librarian's updates, and several settings panes. Paired devices, the desktop window, issues, goals, PRs, rules and the library map keep working.

### 9.1 What we lose from Claude Code

Pi is the only harness (section 0, decision 4). These Claude Code features have no v2 form:

1. **Anthropic's tuning.** Claude Code's system prompt, tool set and tool descriptions are made and tested by Anthropic for each Claude model. Pi's are general. The self-hosting trial compares the work quality on real tasks before the cutover.
2. **Client-side request features.** When it cloaks, the proxy adds Claude Code's beta headers and context-management fields (M0c). Features that need client code, such as tool search or tool changes in the middle of a conversation, work only when Pi implements them.
3. **Built-in tools.** Claude Code's web search and web fetch, its todo list, plan mode and notebook editing are gone. Pi has no web search, so agents have none until the web-search port (section 9).
4. **First access to new Claude features.** New tools, betas and effort levels reach Claude Code first. With Pi, we wait for upstream Pi or add them to our fork.
5. **Terms.** Claude Code is the only client in which Anthropic allows subscription sign-ins. Decision 4 records the accepted risk.

Nothing else is lost. Pi covers steering, follow-up, abort, forks, resume, compaction, MCP (the shell server), extra system prompt text, tool limits and thinking levels. The tool events that v1 read from Claude Code hooks come from Pi's RPC event stream.

## 10. Test strategy

### 10.1 Owner policy

1. The repository holds only solid, worthwhile unit tests. Each is fast (under 1 s), tests one behavior, and uses fakes at the package boundary. No snapshot tests and no implementation-detail tests.
2. The repository holds no end-to-end and no integration tests.
3. When a milestone needs an integration or end-to-end check, a terra agent runs it once by hand in a sandbox (a box on hermeslab or Boat). It reports the result in the PR or the milestone report. Nothing is committed.

### 10.2 Boundaries and their fakes

Each package boundary has one interface and one fake, kept next to the real one.

| Boundary | Real | Fake |
|---|---|---|
| host: SSH master, Podman, systemd | `box/podman` | `box/fakehost`: containers, slices, forwards and command results in memory |
| harness process | `harness/pi` | `harness/fakeharness`: scripted turns (text, tool calls, usage, failure, steering) |
| model upstream | CLIProxyAPI | an `httptest` server |
| GitHub | `engine/git` client | a fake client with the calls the engine makes |
| time | the system clock | an injected clock; no sleeps, no real timeouts, no polling |
| store | SQLite file | the same, in `t.TempDir()`; SQLite is fast enough |

### 10.3 What the unit tests cover

- `room`: pure rules, no fakes needed. Reply channels for each row of the mapping, wake decisions, tree moves, and address resolution.
- `contract`: one table-driven test over the registry: every event body and operation argument decodes from its own encoding, and has a TypeScript type. The stale-generated-file check is a CI step, not a test.
- `engine/store`: the steps from the floor give `schema.sql`; the guard refuses a v1 file with the export step; events publish in commit order; the writer connection never checkpoints.
- `engine/approve`: each decision rule in 3.5, with expiry on the injected clock.
- `box`: admission, transitions, slice names and `podman run` arguments, on the fake host.
- `harness`: each adapter turns recorded harness output into the typed turn result. Recorded output is test input, not a snapshot of our output.
- `web`: `svelte-check` and strict `tsc`, plus unit tests for pure logic only.

A test that needs a real Podman, SSH, harness, GitHub or browser is a manual sandbox check instead.

### 10.4 Where tests run

- Until v2 self-hosts, builders run tests on hermeslab with v1's job runner: `dyna job run --node hermeslab --commit HEAD --workspace -- go test ./...` from `next/` (r19). Locally, only one focused package with `-p 2`.
- CI has one GitHub Actions workflow for `next/**`: `go vet`, `go test ./... -count=1`, the generated-file check, the dependency-direction check, and `pnpm check` for the web app. The whole Go suite stays under 60 seconds on CI. A slow test is fixed at once (r11).
- After self-hosting, tests run inside the builder's own box.

## 11. Milestones

Each milestone ends with a report attached to @root: the unit test results, each manual check with its evidence, and the measured numbers. A manual check is run by a terra agent in a sandbox, as in 10.1.

### M0: the CLIProxy test (done, 2026-10-30)

Result: **yes.** Claude Code 2.1.309 completed a tool-using turn and a resumed turn through a loopback CLIProxyAPI v8.0.48 with only a test client key. Opus, Sonnet and Haiku worked. Prompt caching worked (16,942 cached tokens on the second turn). A wrong key got 401. Three concurrent turns finished. Pi worked through its `anthropic` provider and through an `openai-responses` provider for ChatGPT. The native Pi `openai-codex` provider did not. The proxy does not pass `anthropic-ratelimit-unified-*` headers by default. No refresh token was copied, and no account changed.

M0c result: **yes for Claude, with one fix.** Pi 1.4.0-dyna.10 with `claude-opus-5-5` through the proxy passed three tool turns in one session, caching (96% to 98% of input read from cache after the first turn), compaction, steer, follow-up and abort. Pi sent effort `high` for every thinking level; the fork fixes this in M1. ChatGPT, zai and opencode-go passed tool turns. xAI and deepseek reached their providers and failed only on account balance. Kimi was not tested, because its token was too close to expiry, and CLIProxyAPI supports its sign-in. Muse failed: its key works only in Meta's client. The report records exactly what cloaking changes (5.7). pi-cloud, a similar design, gave the input, retry, restart and affinity rules in 5.7, 5.8 and 5.10.

### M1: the thin slice

Scope: `contract` (3.2 to 3.4, the `Operation` types, `GlobalBundle`, `RoomBundle` and their parts, the M1 methods), `engine/store`, the owner socket and box sockets, the vendored CLIProxyAPI in-process and the model route to it, one host (hermeslab) with admission, `dyna-base`, the box lifecycle without children, the box agent with the Pi adapter, `dyna post` and `dyna room log`.

Acceptance:

1. unit: the store opens fresh from `schema.sql`, refuses a folder with only `dyna.db` with `store_from_v1` and the export step, and publishes events in commit order.
2. unit: the registry test passes, the generated TypeScript is current, `contract` imports only the standard library, and a bundle file with an unknown field fails to decode.
3. unit: a call on box socket A is session A, even when the body names session B. A call on an unknown or closed socket fails. An engine event never resolves to a session.
4. unit: the model route replaces the placeholder key, refuses a model the profile does not allow, and streams server-sent events in order (fake upstream).
5. unit: admission places a box only when requests fit, and otherwise emits `box.changed` to `pending` with a reason (fake host).
6. unit: every refused box transition returns an error, and every allowed one emits one `box.changed`.
7. unit: a reply without a valid `DYNA-END` trailer is a transport error, even when ssh exits 0. The generated proxy config listens only on `127.0.0.1`, sets `request-retry: 0`, keeps strict cloak off, and mounts management only with the engine's key.
8. unit: the Pi adapter turns a recorded RPC event stream into `turn.ended` with the final text and usage, and it sends a steer command while a turn runs.
9. manual: on the desktop, `dyna engine` starts with a fresh store. `dyna session start` puts a Pi session with `claude-opus-5-5` in a box on hermeslab. Its turn runs `dyna post` with a tool and ends. `dyna room log` on the desktop shows `session.started`, `post.created` from the session, and `turn.ended`. With thinking levels xhigh and low, the proxy's request log shows effort `xhigh` and `low` (the fork fix, M0c).
10. manual: inside that box, the environment and the files hold no OAuth token and no CLIProxyAPI client key.
11. manual: `dyna session close` deletes the container, the volume and the slice; `podman ps -a` and `systemctl --user list-units` on hermeslab show nothing left.
12. report: cold and warm box start times, and the time from `session start` to the first model token.

### M2: web room view, approvals, host exec

Scope: the ported web room view (feed, DMs, composer, cards and files, session tree, approvals panel, host-exec grants), the tree with child boxes in the parent's budget, dormant boxes that stop and wake, reply channels (7.5), operations and approvals, host exec, the default notice subscription, images from `.dyna/images/`, and box growth.

Acceptance:

1. unit: each decision rule in 3.5, including expiry on the injected clock and the stale check. A session above the caller can approve, a session beside it cannot, and no test uses a root flag.
2. unit: a session that the owner starts without a parent gets root as its parent. A move that makes a cycle fails. A move to another host, or of a subtree with a running shell job, is refused. During a move, a turn in the subtree that starts a shell job gets a refusal (fake box agent). A move holds the subtree's wakes until its turns end, then re-creates the boxes under the new slices, parent before child, and delivers the held wakes. Close closes the subtree deepest first and gives every open goal and PR to the closing session's parent. When that parent is root, the goals have no holder, and `session.closed` lists them. Close of root fails.
3. unit: an owner DM turn delivers its final response to `dm:<session>` and makes no delivery for the parent. A final response to a target that the turn already posted to is delivered quietly.
4. unit: a child's `podman run` carries `--cgroup-parent` with its slice, and a lead gets a top-level slice counted against the room limit.
5. unit: `box.grow` is auto within the parent's remainder, agent beyond it, and owner for root. A `stopped` box counts only its disk in admission, and a wake that does not fit goes to `pending` (fake host).
6. unit: host-exec argv matching, mode precedence, the class from the matched grant, `no-grant`, revoke, expiry, and the `hostexec-scope` refusal.
7. unit: every host-exec run emits its events in order: `op.requested` when it waits, then `op.decided` and `op.ran`.
8. unit: after a reconnect, the transcript stream resends from the last acknowledged offset with no gap and no copy.
9. manual: in the web app, the owner posts in a room, a boxed session answers, and the answer appears without a reload. An owner DM is answered in the DM. The owner approves an agent-class operation in the approvals panel.
10. manual: root runs `podman ps` on hermeslab through host exec. A child without a grant is refused. The owner grants it for one hour, it runs, and after a revoke it is refused again.
11. manual: a lead and its child run on hermeslab; the child's cgroup is under the lead's slice; closing the lead closes the child first. A dormant session's box stops and starts again on a post.
12. manual: killing a host's master during a turn does not end the turn, and the box returns to `ready` within 15 s.

### M3: bundle import and self-hosting

Scope: rooms with repositories, git through the engine, checkpoints and close with archive, PRs, reviews and merges, CI in a box, goals, issues, questions, rules, external members, hooks as typed subscriptions, engine timers (check-ins, usage), accounts through CLIProxyAPI, settings and backups, `dyna update`, remote access, the desktop window, the import, and the cutover. Section 9 lists every feature.

The order inside M3:

1. Build the features. Each has its unit tests.
2. **Self-hosting trial.** v2 runs beside v1 with its own data, runtime and SSH control folders (decision 8), in a trial room for the `dyna` repository. v2 sessions make the remaining M3 changes through v2 PRs. While v1 still tracks the same repository, v2 names session branches `v2/<session>`, acts only on PRs that v2 sessions opened, and does not wake on unowned repository events. v1 does not track `v2/` branches.
3. **Dry run.** Export from a copy of the latest v1 backup, and import into a scratch v2 store. Compare the counts.
4. **Cutover** (section 8).

Acceptance:

1. unit: the import of a fixture bundle writes every record, and the store counts equal `BundleCounts`. It refuses a wrong version, a missing file, a count that does not match, and a folder that already has `store.db`.
2. unit: the exporter maps `dyna:root`, the owner and engine senders to the right principals on a small fixture.
3. unit: subscriptions fire only on their event type or schedule. One with both `Wake` and `Run`, or neither, fails validation.
4. unit: an unowned `repo.main_updated` for a repository in rooms A (default) and B makes one delivery per room, and only A's root wakes. `repo.settings.set` from a session waits for the owner.
5. unit: a member token reads its inbox, a removed member's token gets `forbidden`, and a member cannot submit `hostexec.run`.
6. unit: the git route refuses a push to a branch that the session does not own. Close archives before it deletes, and stays in `closing` when the archive push fails.
7. unit: `export-state` refuses while the writer runs, and its manifest's last `seq` equals the store's. `import-state` refuses a changed file or a failed integrity check. The old engine refuses to start after an export.
8. manual: during the trial, a v2 session in a box changes `next/`, opens a PR through an operation, CI passes, the review approves, and the merge lands.
9. manual: the dry-run import matches the inventory's keep list for every part, and old CI shows the same `go list -m all` except the `github.com/resident-labs/dyna` line.
10. manual: after the cutover, each room's root starts from its `migrate.handoff` event, and every open goal, PR, issue and question from the bundle is present.
11. manual: Connect works with the copied secrets, and a paired phone opens a room without pairing again. Its posts come from the owner with its `DeviceID`.
12. manual: with a host powered off, its boxes go `lost`. `box.restart` on another host is refused until the owner removes the host. It then resumes from the checkpoint and the transcript copy, and an interrupted tool call is marked, not run again.
13. unit: with a fake CLIProxyAPI management API, `accounts.connect` returns the proxy's link or code, and the store keeps no token or key outside the proxy's token-store table. The SQLite token store round-trips a proxy auth record through `Save`, `List` and `Delete`. A quota below the floor emits one `account.quota_low`, and no second one until the quota recovers and drops again. A provider with no limit from either source shows its counted usage with the limit unknown, and it never emits `account.quota_low`.
14. manual: one full working day in all rooms on v2 with no blocking failure.

## 12. Team and ownership

| Area | Owner | Profile | Folders |
|---|---|---|---|
| Spec, contract, engine core (store, API, approvals, model route), integration | dyna:lead-rewrite | opus-rewrite (r20) | `docs/`, `contract/`, `engine/` |
| Boxes, hosts, images, box agent, harness adapters, host exec | dyna:node-shell | opus | `box/`, `harness/`, `engine/hostexec` |
| Room rules, identity, migration, exporter, feature map | dyna:code-architectural | opus | `room/`, `migrate/`, old `cmd/dyna-export` |
| Web port | a web lead, from M2; design and taste approval by dyna:lead-rewrite (5.13) | sonnet | `web/` |

- **Builders** are terra (r13), started by the area owner after the owner approves this spec. Small work goes to haiku. When terra is out of quota, kimi-max or glm. Each folder has one writer at a time.
- **Design check first.** For each milestone, each area owner writes a one-page plan. The lead checks it before builders start (r13).
- **Reviews** use the v1 review service with terra reviewers until the cutover, then v2's own.
- **Manual checks** are run by one terra agent per milestone, in a sandbox box.
- **v1 is frozen.** Only blocking bugs are fixed there. The old side gets only `cmd/dyna-export`.
- **Reporting.** Area owners report to the lead. The lead reports to @root for decisions, blockers and milestone results.

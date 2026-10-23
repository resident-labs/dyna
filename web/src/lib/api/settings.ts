// Settings keys and hooks. Sources: internal/configsvc/settings.go and
// internal/hooks/hooks.go, runs.go.

// SettingItem is configsvc.Item: one settings key with its effective value.
// kind is bool, enum, select, int, bytes or duration. value is any JSON
// value; display is the text a client shows for it when the engine knows one.
export interface SettingItem {
  key: string
  value: unknown
  source: string
  liveVsRestart: string
  display?: string
  kind: string
  options?: string[]
  optionLabels?: string[]
  help: string
}

// UpdateNotice is configsvc.UpdateNotice: a newer dyna release. text is empty when there is none.
export interface UpdateNotice {
  current: string
  latest: string
  text: string
}

// Hook is hooks.Hook. addedAt and editedAt are ISO times.
export interface Hook {
  name: string
  label?: string
  event: string
  command: string
  cwd: string
  timeout: string
  onFail: string
  // scope says where the hook belongs: one repository, or the whole machine.
  // A hook stored before scope existed belongs to its repository.
  scope?: 'repository' | 'global'
  // builtin names the automatic pass that a hook the engine seeds runs. It is
  // absent on a hook the owner added.
  builtin?: string
  addedBy: string
  addedAt: string
  editedBy?: string
  editedAt?: string
  everyMinutes?: number
  // disabled keeps the hook's settings and stops its automatic runs.
  disabled?: boolean
}

// HookSummary is hooks.Summary: one hook with its newest run, which is what a
// service line needs to name its trigger and its last result.
export interface HookSummary {
  hook: Hook
  lastRun?: HookRun
  // nextRun is when the schedule next runs the hook. It is absent on a hook
  // that does not run on a schedule.
  nextRun?: string
}

// HookRun is hooks.Run, one execution of a hook. finishedAt is the zero time
// until the run ends.
export interface HookRun {
  runnerSession?: string
  id: string
  repo: string
  name: string
  command: string
  result: string
  logPath: string
  startedAt: string
  finishedAt?: string
}

// Device is devicesvc.Device, a paired browser or a Connect sign-in. kind is
// paired or connect. Times are ISO strings.
export interface Device {
  current: boolean
  id: string
  kind: string
  name: string
  created: string
  lastSeen: string
  expires: string
  origin?: string
  device?: string
  scopes?: string[]
  legacy?: boolean
  scopeLabel?: string
}

// HookRunLog is hooks.HookRunLog: the tail of a run's log and its exit code.
export interface HookRunLog {
  tail: string
  exitCode: number
}

// Notice is notices.Notice, a machine alert such as low disk space. The facts
// a card shows are typed per kind: a disk notice carries disk, a provider
// notice carries provider and a sync notice carries sync. text is the stored
// line of a notice saved before the typed facts and stays empty on a new one.
export type NoticeKind = 'disk' | 'provider' | 'sync'
export type NoticeSeverity = 'warn' | 'error'
export interface DiskPath {
  path: string
  freeBytes: number
}
export interface DiskFacts {
  paths: DiskPath[]
  freedBytes?: number
  kept?: string[]
  maintenanceFailed?: boolean
}
export interface ProviderFacts {
  profile: string
  resetsAt?: string
}
export interface SyncFacts {
  repo: string
  lane: string
  other?: string
  rewritten?: boolean
}
export interface Notice {
  id: string
  key: string
  kind: NoticeKind
  severity: NoticeSeverity
  disk?: DiskFacts
  provider?: ProviderFacts
  sync?: SyncFacts
  command?: string
  at: string
  text?: string
}

// Pairing is devicesvc.Pairing.
export interface Pairing {
  code: string
  url: string
  expires: string
}

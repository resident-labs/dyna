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
  addedBy: string
  addedAt: string
  editedBy?: string
  editedAt?: string
  everyMinutes?: number
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

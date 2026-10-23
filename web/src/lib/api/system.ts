// The engine's health and machine load. Sources: internal/engine/health.go,
// internal/state/state.go, internal/hub/hub.go, internal/sysload/sysload.go
// and internal/roomsvc/loadowner.go. Go sends time.Time as an ISO string and
// time.Duration as nanoseconds.

// WriteHealth is state.Health.
export interface WriteHealth {
  writes: number
  p50Ns: number
  p99Ns: number
  slowestNs: number
  inFlight: number
  oldestNs: number
  degraded: boolean
  slowestKind?: string
}

// EventStats is hub.Stats.
export interface EventStats {
  seq: number
  streams: number
  maxLag: number
  dropped: number
}

// Health is engine.Health.
export interface Health {
  started: string
  writes: WriteHealth
  writersWaiting: number
  events: EventStats
  sqliteBusyRetries: number
  callersRefused: number
  sweepFindings: number
  degraded: boolean
  status?: string
  connect?: string
}

// LoadSplit, LoadMemory and LoadTop are sysload.Split, sysload.Memory and
// sysload.Top. Memory is in bytes.
export interface LoadSplit {
  engine: number
  agents: number
  computers: number
  other: number
}

export interface LoadMemory {
  total: number
  used: number
  dyna: number
}

// LoadOwnerCores is roomsvc.LoadOwnerCores: who uses cores. Go embeds LoadOwner.
export interface LoadOwnerCores {
  id: string
  name: string
  hue: number
  cores: number
  address?: string
  room?: string
  roomId?: string
}

// SystemLoad is engine.SystemLoad: sysload.Load with its top list replaced by
// LoadOwnerCores. cpuPercent and split are null until a sample exists.
export interface SystemLoad {
  at: string
  cpus: number
  load: [number, number, number]
  cpuPercent: number | null
  split: LoadSplit | null
  memory: LoadMemory
  top: LoadOwnerCores[] | null
  dynaKnown: boolean
}

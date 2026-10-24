// Computer screens. Sources: internal/computerweb/host.go (Info, App, Event)
// and internal/computers/protocol.go (ActionIntent, Stream).

export type ComputerViewFailureCode = 'wayvnc-missing'

// ComputerViewFailure is a permanent refusal from the stream preflight.
export interface ComputerViewFailure {
  code: ComputerViewFailureCode
  reason: string
  permanent: true
}

// asComputerViewFailure reads both the HTTP error body and the value the API
// client throws. Unknown and transient failures keep the WebSocket retry path.
export function asComputerViewFailure(value: unknown): ComputerViewFailure | null {
  if (typeof value !== 'object' || value === null) return null
  const code: unknown = Reflect.get(value, 'code')
  const reason: unknown = Reflect.get(value, 'reason')
  const permanent: unknown = Reflect.get(value, 'permanent')
  if (code !== 'wayvnc-missing' || typeof reason !== 'string' || !reason || permanent !== true) return null
  return { code, reason, permanent: true }
}

export interface ActionIntent {
  text: string
  at: string
}

// ComputerApp is computerweb.App; the agent protocol struct uses app_id.
export interface ComputerApp {
  appId: string
  title: string
}

export interface ComputerStream {
  running: boolean
  codec: string
  encoder: string
}

// ComputerInfo is computerweb.Info.
export interface ComputerInfo {
  session: string
  name: string
  state: string // see computerweb.Info, for example none, idle or running
  apps: ComputerApp[] | null
  viewers: number
  stream: ComputerStream
  rssKiB: number
  streamRssKiB: number
  latestStatus?: ActionIntent
  room?: string
  reason?: string
  width?: number
  height?: number
  renderer?: string
  control?: string // agent or human
  idleSeconds?: number
  startedAt?: string
  startupMs?: number
  shotAt?: string
}

// ComputerEvent is computerweb.Event on the app event stream.
export interface ComputerEvent {
  kind: 'computer'
  session: string
  type: string
  roomId?: string
  computer?: ComputerInfo
  action?: string
}

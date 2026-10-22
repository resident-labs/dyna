// Computer screens. Sources: internal/computerweb/host.go (Info, App, Event)
// and internal/computers/protocol.go (ActionIntent, Stream).

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

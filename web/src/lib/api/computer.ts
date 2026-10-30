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

// Computer response types come from Go.
export type * from './computer.gen.ts'
export type { App as ComputerApp, Info as ComputerInfo, Stream as ComputerStream, Event as ComputerEvent } from './computer.gen.ts'

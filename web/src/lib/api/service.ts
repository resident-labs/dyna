// ServiceView is roomsvc.ServiceView in internal/roomsvc/services.go.
// ListServices assigns a non-null profiles list to every row.
export interface ServiceView {
  role: string
  name: string
  page: string
  profile: string
  pinned: string
  source: string
  reason: string
  waiting: boolean
  scope: string
  sessions: number
  profiles: string[]
}

// ServicePatch is hooks.ServicePatch: the fields the owner may change on a
// built-in service. A field left out stays as it is.
export interface ServicePatch {
  disabled?: boolean
  everyMinutes?: number
  timeout?: string
}

// The service registry's wire types come from Go (internal/serviceregistry).
export type { Service as RegistryService, ID as RegistryServiceId, Group as RegistryGroup, State as RegistryState, Detail as RegistryDetail } from './service.gen.ts'

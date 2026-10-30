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
  scope: string
  sessions: number
  profiles: string[]
}

// The service registry's wire types come from Go (internal/serviceregistry).
export type { Service as RegistryService, ID as RegistryServiceId, Group as RegistryGroup, State as RegistryState, Detail as RegistryDetail } from './service.gen.ts'

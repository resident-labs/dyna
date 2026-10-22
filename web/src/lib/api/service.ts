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

// Resident Labs Connect and Web Push. Sources: internal/connect/handoff.go,
// internal/connect/renew.go and internal/websvc/push.go. Times are ISO strings.

// Renewal is the answer of /api/connect/renew.
export interface Renewal {
  expires: string
  grantEnd: string
  grace: boolean
}

// Handoff is the answer of /api/connect/handoff: a one-time code.
export interface Handoff {
  code: string
}

// Whoami is the answer of /api/connect/whoami. user, expires and grantEnd are
// set only for a Connect session.
export interface Whoami {
  kind: string
  id: string
  name: string
  user?: string
  expires?: string
  grantEnd?: string
}

// ConnectRoute is connect.Route: an address that reaches this machine.
export interface ConnectRoute {
  kind: string
  app: string
  cards: string
}

// RouteList is the answer of /api/connect/routes.
export interface RouteList {
  machine: string
  key: string
  current: string
  routes: ConnectRoute[]
}

// ConnectSession is the answer of /api/connect/session.
export interface ConnectSession {
  session: string
  expires: string
  grantEnd: string
}

// PushStatus is the answer of /api/push/status.
export interface PushStatus {
  subscribed: boolean
  names: boolean
  key: string
}

// File and Git wire types come from Go.
export type * from './file.gen.ts'

// Requests may omit githubAccess, but Go's non-pointer field has no omitempty.
// Tygo cannot express that request-only optionality without changing Go JSON.
export interface CreateRoomInput {
  githubAccess?: 'read-only' | 'read-write'
  path: string
  name: string
  gitInit: boolean
}

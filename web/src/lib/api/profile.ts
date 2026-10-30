// Profile response types come from Go. ProfileDraft keeps the editor's partial request shape.
import type { ProfileView, ProfileEdit as WireProfileEdit } from './profile.gen.ts'
export type * from './profile.gen.ts'
export type ProfileProblem = 'retired-harness' | 'invalid'
export type ProfileDraft = Omit<ProfileView, 'runtimeVersion' | 'fallback' | 'support'> & Partial<Pick<ProfileView, 'runtimeVersion' | 'fallback' | 'support'>>
export type ProfileEdit = Omit<WireProfileEdit, 'profile'> & { profile: ProfileDraft }

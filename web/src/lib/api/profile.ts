// Profiles as the settings screen edits them. Source: internal/configsvc/profiles.go.
import type { HarnessSupport } from '../settings/support.ts'
import type { Compact } from './account.ts'

// ProfileView is configsvc.ProfileView.
export interface ProfileView {
  runtimeVersion: string
  name: string
  description: string
  harness: string
  model: string
  models?: string[]
  fallback: string
  taste: number
  diagnostics: number
  intelligence: number
  cost: number
  timeoutSec: number
  maxConcurrent: number
  maxCallsPerRun: number
  default: boolean
  disabled: boolean
  uses: string[] | null
  safeMode: boolean
  extraArgs: string[] | null
  compact: Compact | null
  command: string[] | null
  envKeys: string[] | null
  support: HarnessSupport
}

// ProfileCheck is what a live profile check returns: the profile's name and
// its fresh support, plus the probe's own error when it ran and failed.
export interface ProfileCheck extends Pick<ProfileView, 'name' | 'support'> {
  probeError?: string
}

// ProfileList is configsvc.ProfileList. unready maps a harness a new profile
// cannot use yet to the reason.
export interface ProfileList {
  profiles: ProfileView[] | null
  harnesses: string[] | null
  unready: Record<string, string> | null
  default: string
}

// ProfileEdit is configsvc.ProfileEdit. original names the profile being
// edited; empty creates one. envSet adds or replaces values, envRemove drops keys.
export interface ProfileEdit {
  original: string
  profile: ProfileDraft
  envSet: Record<string, string> | null
  envRemove: string[] | null
}

// ProfileDraft is the profile an editor sends: the fields it shows. The
// engine keeps the fields it does not send from the saved profile.
export type ProfileDraft = Omit<ProfileView, 'runtimeVersion' | 'fallback' | 'support'> & Partial<Pick<ProfileView, 'runtimeVersion' | 'fallback' | 'support'>>

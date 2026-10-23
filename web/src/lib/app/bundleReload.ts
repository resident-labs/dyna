import type { Phase } from '../voice/callState.ts'

export interface BundleReloadSafety {
  drafts: Record<string, string>
  composerPending: boolean
  voicePhase: Phase
  visible: boolean
}

// bundleReloadSafe protects work that a page reload would interrupt. An error
// is not a running call; a resumable one still lives in the engine.
export function bundleReloadSafe({ drafts, composerPending, voicePhase, visible }: BundleReloadSafety): boolean {
  const call = ['starting', 'connecting', 'live', 'ending', 'resumable'].includes(voicePhase)
  return visible && !call && !composerPending && Object.values(drafts).every((draft) => draft === '')
}

export interface BundleReload {
  offer(version: string, safe: boolean): boolean
  retry(safe: boolean): boolean
}

// createBundleReload turns repeated build events into one reload. A blocked
// offer stays pending until retry succeeds, or the owner explicitly reloads.
export function createBundleReload(reload: () => void): BundleReload {
  let pending = false
  let reloaded = false
  const run = (safe: boolean): boolean => {
    if (!pending || reloaded || !safe) return false
    reloaded = true
    reload()
    return true
  }
  return {
    offer(version, safe) {
      if (!version || reloaded) return false
      pending = true
      return run(safe)
    },
    retry: run,
  }
}

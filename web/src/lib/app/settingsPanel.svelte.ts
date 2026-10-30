import type { HookLogTarget } from '../settings/hookLog.ts'
import type { ModelSelection } from '../settings/profiles/state.ts'

export interface SettingsPanelHost {
  hasRoom(): boolean
  closeNav(): void
  roomRequired(): void
  // openServices shows the open room's Services page.
  openServices(): void
}

// HooksFocus asks the Services page to show its Hooks section. request counts
// the asks, so each one scrolls there again; run is the hook run to open.
export interface HooksFocus {
  request: number
  run: HookLogTarget | null
}

export function createSettingsPanel(host: SettingsPanelHost) {
  let showSettings = $state(false)
  let showRoomSettings = $state(false)
  let settingsKey = $state(0)
  let roomSettingsKey = $state(0)
  let settingsAt = $state<{ start: string; pairNow: boolean; profile?: string; accountId?: string; initialSelection?: ModelSelection }>({ start: 'profiles', pairNow: false })
  let hooksAt = $state<HooksFocus>({ request: 0, run: null })

  return {
    get showSettings() { return showSettings },
    get showRoomSettings() { return showRoomSettings },
    get settingsKey() { return settingsKey },
    get roomSettingsKey() { return roomSettingsKey },
    get settingsAt() { return settingsAt },
    get hooksAt() { return hooksAt },
    closeSettings() { showSettings = false },
    closeRoomSettings() { showRoomSettings = false },
    openSettings(start = 'profiles', pairNow = false, profile = '', accountId = '') {
      host.closeNav()
      settingsAt = { start, pairNow, profile, accountId }
      settingsKey++
      showSettings = true
    },
    openRoomSettings() {
      roomSettingsKey++
      showRoomSettings = true
    },
    // openHooks closes the settings panels and shows Services > Hooks, with
    // one hook run open when a feed card asked for it. Hooks live on a room's
    // Services page, so it needs an open room.
    openHooks(run: HookLogTarget | null = null) {
      host.closeNav()
      if (!host.hasRoom()) { host.roomRequired(); return }
      showSettings = false
      showRoomSettings = false
      hooksAt = { request: hooksAt.request + 1, run }
      host.openServices()
    },
    createProfile(selection: ModelSelection) {
      settingsAt = { start: 'profiles', pairNow: false, initialSelection: selection }
      settingsKey++
      showSettings = true
    },
  }
}

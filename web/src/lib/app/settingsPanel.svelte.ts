import type { HookLogTarget } from '../settings/hookLog.ts'
import type { ModelSelection } from '../settings/profiles/state.ts'

export interface SettingsPanelHost {
  hasRoom(): boolean
  closeNav(): void
  roomRequired(): void
}

export function createSettingsPanel(host: SettingsPanelHost) {
  let showSettings = $state(false)
  let showRoomSettings = $state(false)
  let settingsKey = $state(0)
  let roomSettingsKey = $state(0)
  let settingsAt = $state<{ start: string; pairNow: boolean; profile?: string; accountId?: string; initialSelection?: ModelSelection }>({ start: 'profiles', pairNow: false })
  let roomSettingsAt = $state<{ start: string; hookRun?: HookLogTarget | null }>({ start: 'general' })

  return {
    get showSettings() { return showSettings },
    get showRoomSettings() { return showRoomSettings },
    get settingsKey() { return settingsKey },
    get roomSettingsKey() { return roomSettingsKey },
    get settingsAt() { return settingsAt },
    get roomSettingsAt() { return roomSettingsAt },
    closeSettings() { showSettings = false },
    closeRoomSettings() { showRoomSettings = false },
    openSettings(start = 'profiles', pairNow = false, profile = '', accountId = '') {
      host.closeNav()
      settingsAt = { start, pairNow, profile, accountId }
      settingsKey++
      showSettings = true
    },
    openRoomSettings() {
      roomSettingsAt = { start: 'general' }
      roomSettingsKey++
      showRoomSettings = true
    },
    openHookLog(target: HookLogTarget) {
      host.closeNav()
      if (!host.hasRoom()) { host.roomRequired(); return }
      showSettings = false
      roomSettingsAt = { start: 'hooks', hookRun: target }
      roomSettingsKey++
      showRoomSettings = true
    },
    createProfile(selection: ModelSelection) {
      settingsAt = { start: 'profiles', pairNow: false, initialSelection: selection }
      settingsKey++
      showSettings = true
    },
  }
}

// The page's shortcut handler reads live state. A dialog or menu owns Escape
// before selection, transcript panels, and the phone drawer do.
export interface ShortcutHost {
  paletteOpen(): boolean
  openSearch(): void
  closeSearch(): void
  toggleSidebar(): void
  selectingSessions(): boolean
  toggleSessionSelection(): void
  drillOpen(): boolean
  threadsOpen(): boolean
  closeThreads(): void
  viewing(): boolean
  cardOpen(): boolean
  goalOpen(): boolean
  closeGoal(): void
  pinsOpen(): boolean
  closePins(): void
  narrow(): boolean
  closePhonePanels(): void
}

export function createShortcut(host: ShortcutHost) {
  return (e: KeyboardEvent) => {
    const inside = (selector: string) => !!(e.target as Element | null)?.closest?.(selector)
    if (e.key === 'Escape' && (e.defaultPrevented || inside('[role="menu"], [role="dialog"], [role="alertdialog"]'))) return
    if (e.key.toLowerCase() === 'k' && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey) {
      e.preventDefault()
      if (host.paletteOpen()) host.closeSearch()
      else host.openSearch()
      return
    }
    if (e.key.toLowerCase() === 'b' && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey) {
      e.preventDefault()
      host.toggleSidebar()
    }
    if (e.key === 'Escape' && host.selectingSessions()) { e.preventDefault(); host.toggleSessionSelection(); return }
    if (e.key === 'Escape' && host.drillOpen() && host.threadsOpen() && !host.viewing() && !host.cardOpen() && !host.paletteOpen()) { host.closeThreads(); return }
    if (e.key === 'Escape' && host.goalOpen() && !host.viewing() && !host.cardOpen() && !host.paletteOpen()) host.closeGoal()
    if (e.key === 'Escape' && host.pinsOpen() && !host.viewing() && !host.cardOpen() && !host.paletteOpen()) host.closePins()
    if (e.key === 'Escape' && host.narrow()) host.closePhonePanels()
  }
}

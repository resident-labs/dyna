// Keep data refreshes outside the key so they do not remount the active view.
export function viewIdentity({ roomId, view, drill }: { roomId: string; view: string; drill?: { id?: string } | null }): string {
  return JSON.stringify([roomId, view, drill?.id ?? ''])
}

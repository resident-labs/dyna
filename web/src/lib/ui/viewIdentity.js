// Keep data refreshes outside the key so they do not remount the active view.
export function viewIdentity({ roomId, view, drill }) {
  return JSON.stringify([roomId, view, drill?.id ?? ''])
}

// The session menu carries one Expedite item for the goals an agent holds.
// A session with one goal expedites it at once; a session with several goals
// opens a picker, where the room's current expedited goal carries a check.
export interface ExpediteMenuItem {
  label: string
  hint?: string
  mode: 'direct' | 'choose'
}

export function expediteMenuItem(held: readonly { id: string }[], expeditedId: string): ExpediteMenuItem | null {
  const [first, second] = held
  if (!first) return null
  if (!second) return first.id === expeditedId ? { label: 'Stop expediting', mode: 'direct' } : { label: `Expedite ${first.id}`, mode: 'direct' }
  const current = held.some((g) => g.id === expeditedId)
  return current ? { label: 'Expedite…', hint: `✓ ${expeditedId}`, mode: 'choose' } : { label: 'Expedite…', mode: 'choose' }
}

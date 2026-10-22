// A compaction row in the DM and the session view. The transcript sends one
// typed item for it (internal/transcript), so no view matches its words.

export interface CompactionItem {
  // The harness is compacting (started), finished (completed) or gave up
  // (failed, with the reason the harness gave).
  state?: string
  // Why it ran: auto, manual, or dyna when Dyna ran the compact command.
  trigger?: string
  // The context size on each side, when the harness reports it.
  tokensBefore?: number
  tokensAfter?: number
  reason?: string
}

// compactTokens words a context size in a divider: 999, 182k, 1.1M. The
// plain view's transcript.CompactTokens must print the same string for the
// same event, so both round and both use an upper-case M.
export function compactTokens(n: number | undefined): string {
  if (!n) return ''
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`
  if (n >= 1e3) return `${Math.round(n / 1e3)}k`
  return String(n)
}

// compactionText words one divider, for example
// "Context compacted · 182k → 41k tokens · auto".
export function compactionText(row: CompactionItem): string {
  if (row.state === 'started') return 'Compacting context…'
  if (row.state === 'failed') return row.reason ? `Context compaction failed: ${row.reason}` : 'Context compaction failed'
  const parts: string[] = []
  const after = compactTokens(row.tokensAfter)
  if (row.tokensBefore && after) parts.push(`${compactTokens(row.tokensBefore)} → ${after} tokens`)
  else if (after) parts.push(`${after} tokens after`)
  if (row.trigger) parts.push(row.trigger)
  return ['Context compacted', ...parts].join(' · ')
}

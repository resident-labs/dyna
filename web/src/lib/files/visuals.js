// A visualization is a chain of card posts. Every card shows the newest
// version of its visual; api.visuals lists each visual with that version
// and every older one.

// byVisual maps each visual ID to its row: {id, post, attachment, versions}.
export function byVisual(list) {
  return Object.fromEntries((list ?? []).map((v) => [v.id, v]))
}

// visualOf is the row of the visual that att, a card in post postId,
// belongs to, or null before the list has loaded it.
export function visualOf(rows, postId, att) {
  return rows[att.visual || postId] ?? null
}

// newestVersion is the card to show for att.
export function newestVersion(rows, postId, att) {
  return visualOf(rows, postId, att)?.attachment ?? att
}

// checkStatus is the worst status among a card's checks: fail, warn, pass,
// or skip when every check was skipped. A card without checks is 'none'.
export function checkStatus(att) {
  const found = new Set((att?.checks ?? []).map((c) => c.status))
  for (const s of ['fail', 'warn', 'pass', 'skip']) if (found.has(s)) return s
  return 'none'
}

// checksSummary reads "4 passed, 1 warning", or the reason there is none.
export function checksSummary(att) {
  const count = (s) => (att?.checks ?? []).filter((c) => c.status === s).length
  const parts = [[count('pass'), 'passed'], [count('warn'), 'warning'], [count('fail'), 'failed']]
    .filter(([n]) => n).map(([n, word]) => `${n} ${word === 'warning' && n > 1 ? 'warnings' : word}`)
  if (parts.length) return parts.join(', ')
  return att?.checks?.length ? 'Checks skipped' : 'No checks'
}

// filterVisuals keeps the rows a gallery filter shows: all, mine (the owner
// posted the newest version), or those whose newest version needs attention.
export function filterVisuals(rows, filter) {
  if (filter === 'mine') return rows.filter((v) => v.post.owner)
  if (filter === 'attention') return rows.filter((v) => ['warn', 'fail'].includes(checkStatus(v.attachment)))
  return rows
}

// updates reports a card posted as a later version of a visual. Chat shows
// it as a one-line chip; the banner stays on the post that created it.
export const updates = (att) => !!att.visual

// placeholderHue picks one of the eight name hues for a card with no
// preview image, the same for the same title everywhere.
export function placeholderHue(title) {
  let h = 0
  for (const c of title || '') h = (h * 31 + c.charCodeAt(0)) >>> 0
  return h % 8
}

// versionOf is the version numbered n, else the newest. Two versions can
// share bytes (a revert), so the number, not the attachment ID, names one.
export function versionOf(versions, n) {
  return versions.find((v) => (v.attachment.version || 1) === n) ?? versions[0]
}

// Only HTML attachments change the visual list. Missing rows require a refresh.
export function changesVisuals(event) {
  return !event.posts || event.posts.some((post) => post.attachments?.some((att) => att.render === 'html'))
}

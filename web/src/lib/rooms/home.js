// What the Dyna home screen shows. The functions are pure; Home.svelte loads
// the data and draws the result.
import { accountLabel, accountStatus, needsSignIn, remainingPercent, SUBSCRIPTIONS, usageGroups } from '../settings/usage/state.ts'
import { HARNESS_ORDER, harnessRowState } from '../settings/harnesses/state.ts'

// accountRows splits the active accounts: one that works and reports usage
// is a usage card, one that needs a login or is quota-blocked is an alert row.
// Known subscriptions also show their period when no percentage is reported.
export function accountRows(snapshot) {
  const cards = []
  const alerts = []
  for (const a of snapshot?.accounts ?? []) {
    if (!a.active) continue
    const login = needsSignIn(a)
    if (login || a.quotaBlocked) alerts.push({ id: a.id, text: `${accountLabel(a)}: ${accountStatus(a)}`, action: login ? 'Log in' : 'Details' })
    else if (SUBSCRIPTIONS.some(sub => sub.id === a.plan?.id) || a.windows?.some((w) => remainingPercent(w) !== null)) cards.push(a)
  }
  return { cards: usageGroups(cards), alerts }
}

// updateRows lists what has a newer version: Dyna itself (an update notice
// with a latest version) and each installed harness whose recommended
// version is newer than Dyna's copy.
export function updateRows(notice, runtimes) {
  const updates = notice?.latest ? [{ id: 'dyna', name: 'dyna', current: notice.current, latest: notice.latest }] : []
  if (runtimes) {
    for (const h of HARNESS_ORDER) {
      const row = harnessRowState(h.id, runtimes)
      if (row.update) updates.push({ id: h.id, name: h.id, current: row.version, latest: row.update })
    }
  }
  return updates
}

// rootQuestions lists the open questions root asked the human. Agents ask
// root, not the human, so the home screen shows nothing else that waits.
export function rootQuestions(rooms, questionsByRoom) {
  const rows = []
  for (const room of rooms) {
    if (room.archived) continue
    for (const q of questionsByRoom[room.id] ?? []) {
      if (q.state === 'open' && q.to === 'owner' && q.asker === 'owner' && !q.human) rows.push({ key: `${room.id}:${q.id}`, room, text: q.text })
    }
  }
  return rows
}

// allClear reports whether Home may say nothing needs the owner: every source
// has loaded successfully, and none has a card, question, alert or update.
// A source that is still loading, or failed, never counts as empty.
export function allClear({ usage, runtimes, questionsLoaded, noticesLoaded, cards, asked, alerts, updates }) {
  return !!(usage && runtimes && questionsLoaded && noticesLoaded) && !cards && !asked && !alerts && !updates
}

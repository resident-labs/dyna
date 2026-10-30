import { copyText } from '../../api.ts'
import type { api } from '../../api.ts'
import { errorText } from '../errorText.ts'
import { isFinished } from '../rooms/memberOrder.ts'
import { selectableSession } from '../sessions/consolidation.ts'
import type { Member } from '../api/room.ts'
import type { Goal } from '../api/goal.ts'
import type { ToastInput } from '../ui/toast.ts'
import type { Confirming } from './confirm.svelte.ts'

interface MenuItem {
  label: string
  run: () => unknown
  hint?: string
  danger?: boolean
  gap?: boolean
  disabled?: boolean
}

interface Host {
  api: Pick<typeof api, 'reopenSession' | 'dismissSession' | 'returnSeat'>
  room(): string
  request(): number
  sessionVisit(): number
  selectedId(): string | undefined
  patchMember(member: Awaited<ReturnType<typeof api.reopenSession>>): void
  loadSession(): Promise<unknown>
  loadMembers(): Promise<unknown>
  loadProfiles(): void
  openSession(member: Member): void
  openMember(member: Member): void
  mentionMember(member: Member): void
  selectForConsolidation(member: Member): void
  moveUnder(member: Member): void
  expediteItems(member: Member, expeditedId: string): MenuItem[]
  askConfirm(confirm: Confirming): void
  say(input: ToastInput): void
}

export function createSessionActions(host: Host) {
  let menu = $state<{ m: Member; x: number; y: number; anchor?: HTMLElement } | null>(null)
  let transferring = $state<Member | null>(null)
  let expediting = $state<{ member: Member; goals: Goal[] } | null>(null)
  let reopening = $state('')
  let dismissing = $state('')

  async function reopenSession(m: Member) {
    if (reopening || dismissing || !m.closed || !m.resumable) return
    const rid = host.room(), visit = host.request(), selection = host.sessionVisit()
    reopening = m.id ?? ''
    try {
      if (!m.id) return
      const member = await host.api.reopenSession(rid, m.id)
      if (rid !== host.room() || visit !== host.request()) return
      if (host.selectedId() === m.id && selection === host.sessionVisit()) {
        host.patchMember(member)
        await host.loadSession()
      }
      await host.loadMembers()
      host.say({ tone: 'success', subject: `${m.name} is open`, text: 'Its earlier turns are in the transcript.', action: m.id ? { label: 'Open', run: () => host.openSession(member) } : undefined })
    } finally { reopening = '' }
  }
  async function takeBackSeat() {
    const rid = host.room()
    try {
      await host.api.returnSeat(rid)
    } catch (e) {
      host.say({ tone: 'error', subject: 'Could not take back the root seat', text: errorText(e) })
      return
    }
    if (rid === host.room()) await host.loadMembers()
  }
  async function dismissSession(m: Member) {
    if (dismissing || reopening || !isFinished(m)) return
    const rid = host.room(), visit = host.request(), selection = host.sessionVisit()
    dismissing = m.id ?? ''
    try {
      if (!m.id) return
      const member = await host.api.dismissSession(rid, m.id)
      if (rid !== host.room() || visit !== host.request()) return
      if (host.selectedId() === m.id && selection === host.sessionVisit()) {
        host.patchMember(member)
        await host.loadSession()
      }
      await host.loadMembers()
      host.say({ tone: 'success', subject: `${m.name} is closed`, text: 'Its transcript stays available under closed sessions.' })
    } finally { dismissing = '' }
  }
  function menuItems(m: Member, expeditedId: string) {
    const items: MenuItem[] = [
      { label: m.closed ? 'Open transcript' : 'Message', run: () => host.openSession(m) },
      { label: 'Open profile', run: () => host.openMember(m) },
    ]
    if (isFinished(m)) items.push({ label: dismissing === m.id ? 'Dismissing…' : 'Dismiss', run: () => dismissSession(m).catch((e) => host.say({ tone: 'error', subject: `Could not dismiss ${m.name}`, text: errorText(e) })) })
    if (m.closed && m.resumable) items.push({ label: reopening === m.id ? 'Reopening…' : 'Reopen', run: () => reopenSession(m).catch((e) => host.say({ tone: 'error', subject: `Could not reopen ${m.name}`, text: errorText(e) })) })
    if (!m.closed && m.role !== 'root') items.push({ label: 'Consolidate sessions…', run: () => host.selectForConsolidation(m) })
    if (!m.closed) items.push({ label: 'Mention in composer', run: () => host.mentionMember(m) })
    items.push(...host.expediteItems(m, expeditedId))
    items.push({ label: 'Copy address', hint: m.address, run: () => copyText(m.address).then(
      () => host.say({ tone: 'success', subject: 'Copied the address', text: `Paste it to mention ${m.name}.` }),
      (e) => host.say({ tone: 'error', subject: 'Could not copy the address', text: errorText(e) }),
    ) })
    if (!m.closed) items.push({ label: m.transfer ? 'Model transfer…' : 'Transfer to new model…', run: () => { host.loadProfiles(); transferring = m } })
    if (!m.closed && m.parent) items.push({ label: 'Promote to lead', run: () => host.askConfirm({ kind: 'promote', m }) })
    if (selectableSession(m)) items.push({ label: 'Move under…', run: () => host.moveUnder(m) })
    if (m.state === 'live') items.push({ label: 'Stop turn', danger: true, gap: true, run: () => host.askConfirm({ kind: 'stop', m }) })
    if (!m.closed) items.push({ label: 'Close session', danger: true, gap: m.state !== 'live', run: () => host.askConfirm({ kind: 'close', m }) })
    return items
  }
  return {
    get menu() { return menu },
    get transferring() { return transferring },
    get expediting() { return expediting },
    get reopening() { return reopening },
    get dismissing() { return dismissing },
    openMenu(m: Member, x: number, y: number, anchor?: HTMLElement) { menu = { m, x, y, anchor } },
    closeMenu() { const anchor = menu?.anchor; menu = null; anchor?.focus?.() },
    dismissMenu() { menu = null },
    setTransferring(member: Member | null) { transferring = member },
    setExpediting(value: { member: Member; goals: Goal[] } | null) { expediting = value },
    menuItems, reopenSession, dismissSession, takeBackSeat,
  }
}

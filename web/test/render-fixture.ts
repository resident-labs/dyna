import { render } from 'svelte/server'
import Skeleton from '../src/lib/ui/Skeleton.svelte'
import Session from '../src/lib/sessions/Session.svelte'
import Feed from '../src/lib/rooms/Feed.svelte'
import DmMessage from '../src/lib/sessions/DmMessage.svelte'
import { setVisuals } from '../src/lib/files/visuals.svelte.ts'
import type { CallRequest } from '../src/lib/voice/callRequests.ts'
import type { Attachment, Post, Room } from '../src/lib/api/room.ts'

const room: Room = { id: 'room_20260101-000000-abcdef12', name: 'alpha', lastActivity: '', unread: 0, running: 0, members: 0, pinned: false, pinOrder: 0, archived: false }
const id = '225353899c144463bce35c27'
const noop = (): void => {}
export const manifest = `Attachments (1), saved with the room:\n- home-design.md · text/markdown · 6.1 KB · /home/a/rooms/r/files/${id}.txt\nOpen each file with your file-reading tool.\nIf a path does not open: dyna attachment get r <id> prints it, and -o - writes the bytes.`

export function skeleton(): string {
  return render(Skeleton, { props: { label: 'Loading room', rows: 3 } }).body
}

export function loadingSession(): string {
  return render(Session, { props: { room, session: null, onsend: async () => undefined, onopenfile: noop, onchild: noop, onpr: noop, onjob: noop } }).body
}

const post = (fields: Partial<Post>): Post => ({
  id: 'p1', roomId: room.id, kind: 'message', ts: 1, wake: false,
  from: 'alpha:worker', to: ['alpha'], text: 'Plan is ready.',
  owner: false, completion: false, fromHue: 0, speaker: 'worker',
  ...fields,
})

const feed = (p: Post | null, calls: CallRequest[] = []): string => render(Feed, { props: { room, posts: p ? [p] : [], calls, onearlier: noop, onnewer: noop, onpresent: noop, onaround: noop, onaddress: noop, onreveal: noop, onreply: noop, onforward: noop, onpin: noop, onopenfile: noop, ondetail: noop, onquestion: noop, onacceptcall: noop, ondeclinecall: noop, ongoal: noop } }).body
const dm = (p: Post): string => render(DmMessage, {
  props: { room, address: 'alpha:other', member: undefined, onopenfile: noop, row: { kind: 'message', key: p.id, author: p.from, ts: p.ts, text: p.text, post: p, cont: false }, inThread: true },
}).body
const raw = (text: string): string => render(DmMessage, {
  props: { room, address: 'alpha:other', member: undefined, onopenfile: noop, row: { kind: 'message', key: 'raw', author: 'alpha:worker', ts: 1, text, itemKind: 'steering' } },
}).body

export function attachmentRenderings(): string[] {
  const attachment: Attachment = { id, name: 'home-design.md', mime: 'text/markdown', size: 6246, sha256: '' }
  const stored = post({ attachments: [attachment] })
  const delivered = post({ id: 'p2', text: `Plan is ready.\n\n${manifest}` })
  return [feed(stored), dm(stored), feed(delivered), dm(delivered)]
}

export function rawAttachment(): string {
  return raw(manifest)
}

const cardText = `Plan\n\nAttachments (1), saved with the room:\n- plan.html · HTML card "Plan" · 900 B · /f/${id}.bin\nOpen each file with your file-reading tool.`

export function htmlCard(): string {
  return raw(cardText)
}

export function visualUpdate(): string {
  return raw(cardText.replace('HTML card "Plan"', 'HTML card "Plan", visual msg_0123456789abcdef v3'))
}

export function currentVisualTitle(): string {
  const newest: Attachment = {
    id: '0123456789abcdef01234567', name: 'ci3.html', mime: 'text/html', size: 900, sha256: '',
    render: 'html', title: 'CI time per PR', visual: 'msg_0000000000000001', version: 3,
  }
  setVisuals(room.id, [{ id: 'msg_0000000000000001', post: post({ id: 'msg_0000000000000003', ts: 1 }), attachment: newest, versions: [] }])
  return feed(post({
    id: 'msg_0000000000000002', text: 'v2',
    attachments: [{ ...newest, id: '0123456789abcdef01234568', name: 'ci2.html', title: 'ci2', version: 2 }],
  }))
}

export function callOnlyFeed(): string {
  return feed(null, [{ id: 'c1', room: room.id, reason: 'Review the call request', state: 'ringing', askedAt: '2026-10-20T10:00:00Z' }])
}

export function emptyFeed(): string {
  return feed(null)
}

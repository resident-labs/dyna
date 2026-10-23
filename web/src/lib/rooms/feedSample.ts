// Sample posts for the feed preview in Settings and the feed fixture. They
// show each feed choice: a worker result, root's result, a reply, the
// owner's own post and a failure.
import type { Post, Room } from '../api/room.ts'

// A sample post is a stored post whose recipient list is never empty, so the
// look helpers, which read a post that may have none, accept it too.
export type SamplePost = Omit<Post, 'to'> & { to: string[] }

const t = Date.parse('2026-10-19T13:17:00Z')
let n = 0
const post = (from: string, speaker: string, text: string, extra: Partial<SamplePost> = {}): SamplePost => ({ id: `sample_${++n}`, roomId: sampleRoom.id, wake: false, ts: t + n * 60000, from, speaker, text, to: ['dyna-agent'], fromHue: n % 8, kind: 'message', owner: false, completion: false, ...extra })
const done = (who: string, turn: number, text: string, kind = 'completed'): SamplePost => post(`dyna-agent:${who}`, who, `dyna-agent:${who} turn ${turn} ${kind}. Result: ${text}`, { kind, completion: true, turn })

export const sampleRoom: Room = { id: 'preview', name: 'dyna-agent', lastActivity: '', unread: 0, running: 0, members: 1, pinned: false, pinOrder: 0, archived: false }

export const samplePosts: SamplePost[] = [
  done('ui-guy-right', 464, 'No action needed. #866, the Computers empty state, is ready and merges on its own.'),
  done('root', 270, 'Merged #864. The Harnesses screen now shows Install, Sign in and Update only when they apply.'),
  post('dyna-agent:settings-polish', 'settings-polish', 'All four cleanups are pushed to #868.', { to: ['dyna-agent:root'] }),
  done('settings-polish', 3, 'The Install change is ready. A harness that is not installed shows Install as its only action.'),
  post('dyna-agent', 'you', 'Nice. Ship it.', { human: true, owner: true, to: ['dyna-agent:root'] }),
  done('engine-lead', 51, 'The build broke on a missing import.', 'failed'),
]

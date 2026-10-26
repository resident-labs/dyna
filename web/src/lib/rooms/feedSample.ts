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

// Workflow script posts for the feed fixture (?workflow=1): the data form, a
// post from before runs were stored on the post, and a long inline value.
const runPost = (text: string, extra: Partial<SamplePost>): SamplePost => post('run:wf_20261026-120344-604cf9b5', 'run:wf_20261026-120344-604cf9b5', text, { roomId: 'room_20260101-000000-abcdef12', to: ['dyna-agent:integrator'], ...extra })
export const workflowPosts = (): SamplePost[] => [
  runPost('**Landing request** after completed review.\n\n```json\n{\n  "unit": "CH",\n  "nativeHead": "31cdb141c7d5f65c978a282ebb2a5ff71eeeb41d",\n  "reviewEvidence": "/home/ari/.cache/horizon-swarm/reviews/continuous-recovered-1204-ch-CH-a1-r0.md",\n  "approvalPolicy": "independent sol exact-head approval",\n  "provisional": false,\n  "followups": ["CL/W0 must complete runtime wiring, state-coverage validation, vanilla-reference resolution, and host-context selection."]\n}\n```\nRun serialized acceptance. Do not dispatch another controller.', { script: { runId: 'wf_20261026-120344-604cf9b5', workflow: 'landing-controller' } }),
  runPost('LANDING REQUEST after completed review and recorded completion token: {"nativeBase":"21733e581601cef5a6258cd8e1dc7d1631800d7c","engineBranch":"swarm/continuous-wave5-1115-ch-ch-engine","scopeNote":"","provisional":false}\nRun serialized acceptance.', {}),
]

export const samplePosts: SamplePost[] = [
  done('ui-guy-right', 464, 'No action needed. #866, the Computers empty state, is ready and merges on its own.'),
  done('root', 270, 'Merged #864. The Harnesses screen now shows Install, Sign in and Update only when they apply.'),
  post('dyna-agent:settings-polish', 'settings-polish', 'All four cleanups are pushed to #868.', { to: ['dyna-agent:root'], forRoot: true }),
  done('settings-polish', 3, 'The Install change is ready. A harness that is not installed shows Install as its only action.'),
  post('dyna-agent', 'you', 'Nice. Ship it.', { human: true, owner: true, to: ['dyna-agent:root'] }),
  done('engine-lead', 51, 'The build broke on a missing import.', 'failed'),
]

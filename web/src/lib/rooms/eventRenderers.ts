// The web side of the system event registry. events.gen.ts is generated from
// internal/room/events.go by `DYNA_UPDATE_EVENTS=1 go test ./internal/room/
// -run Events`, so the payload types of an event and its card cannot drift.
//
// A renderer takes { post, notice, surface } and reads the typed payload only:
// no text parsing and no feed-only state. The feed and a session DM draw the
// same card (system-events.md section 3.4.2). The optional props carry the
// room's post bar, so a card in the feed keeps the actions a card in a DM does
// not need.
import type { Component } from 'svelte'
import WorktreeCard from './WorktreeCard.svelte'
import HookCard from './HookCard.svelte'
import ShellResultRow from './ShellResultRow.svelte'
import ShellSilenceCard from './ShellSilenceCard.svelte'
import RunResultCard from './RunResultCard.svelte'
import JobCard from './JobCard.svelte'
import TurnCompletionRow from './TurnCompletionRow.svelte'
import MergeCard from './MergeCard.svelte'
import MachineDiskCard from './MachineDiskCard.svelte'
import MachineCacheCard from './MachineCacheCard.svelte'
import MachineCacheCleanCard from './MachineCacheCleanCard.svelte'
import MachineScratchCard from './MachineScratchCard.svelte'
import MachineLimitsCard from './MachineLimitsCard.svelte'
import RoomCapCard from './RoomCapCard.svelte'
import WorktreePortsCard from './WorktreePortsCard.svelte'
import CallCard from './CallCard.svelte'
import VoiceSessionCard from './VoiceSessionCard.svelte'
import PRCard from '../prs/PRCard.svelte'
import IssueCard from '../issues/IssueCard.svelte'
import GoalPost from '../goals/GoalPost.svelte'
import CheckinCard from './CheckinCard.svelte'
import SlowTestCard from './SlowTestCard.svelte'
import RuleCard from './RuleCard.svelte'
import SessionNoteCard from './SessionNoteCard.svelte'
import InstructionBatchCard from './InstructionBatchCard.svelte'
import AlertCard from './AlertCard.svelte'
import ServiceAlertCard from './ServiceAlertCard.svelte'
import ServiceWaitCard from './ServiceWaitCard.svelte'
import HelperOutcomeRow from './HelperOutcomeRow.svelte'
import MCPCallRow from './MCPCallRow.svelte'
import EngineNoticeCard from './EngineNoticeCard.svelte'
import QuestionNoteCard from './QuestionNoteCard.svelte'
import TerminalRequestCard from './TerminalRequestCard.svelte'
import ReviewResultCard from './ReviewResultCard.svelte'
import { eventSpecs } from './events.gen.ts'
import type { EventSpecEntry } from './events.gen.ts'
import type { EventNotice, Question, Room } from '../api/room.ts'
import type { EventPayload } from './events.gen.ts'
import type { HookFold } from './feedRows.ts'
import type { PostSelection } from '../ui/postSelect.ts'
export { sealedEvent } from './eventPayloads.ts'

export type EventSurface = 'feed' | 'dm'

// EventPost is the part of a stored post a card reads. A stored message, a
// feed row and a DM row all fit it, so one card serves both surfaces.
export interface EventPost {
  id: string
  ts?: string | number
  text?: string
  kind?: string
  from?: string
  fromHue?: number
  // speaker, completion, fullResult and turn are what a feed row and a DM row
  // keep of the shaped post; a card shows them when its event has no better
  // name for the session.
  speaker?: string
  completion?: boolean
  fullResult?: boolean
  turn?: number
  notice?: EventNotice
  payload?: EventPayload
  to?: string[] | null
}

export interface EventProps {
  post: EventPost
  notice?: EventNotice
  surface?: EventSurface
  // posts is the folded posts of one feed row, oldest first.
  posts?: EventPost[]
  // hookFold is the fold of a row of hook passes: its count and the time of
  // its last pass. A card of one post has none.
  hookFold?: HookFold
  room?: Room
  pinned?: Set<string>
  selection?: PostSelection | null
  onreply?: (post: EventPost) => void
  onforward?: ((post: EventPost) => void) | null
  onpin?: (post: EventPost) => void
  to?: string[]
  onaddress?: (to: string) => void
  showRepo?: boolean
  focus?: string
  // goals and ongoal open a goal from its row, as the feed does.
  goals?: readonly GoalRef[]
  ongoal?: (id: string) => void
  // onissue opens the room's Issues panel, where an issue's actions live.
  onissue?: (issue: string) => void
  // request is the live question behind a terminal request post, and the
  // owner's two actions on it: open its review in the dock, or decline it.
  request?: Question
  onreviewrequest?: (id: string) => void
  ondeclinerequest?: (id: string, reason: string) => Promise<unknown>
}

// GoalRef is the part of a goal a card reads.
export interface GoalRef {
  id: string
  text?: string
}

export type EventRenderer = Component<EventProps>

// Components by the name a spec gives in its renderer field. A name with no
// component here draws the unknown event row, and the test fails.
export const eventComponents: Record<string, EventRenderer> = {
  WorktreeCard, HookCard, PRCard, MergeCard, IssueCard, GoalPost, CheckinCard, SlowTestCard, RuleCard, MachineDiskCard, MachineCacheCard, MachineCacheCleanCard, MachineScratchCard, MachineLimitsCard, RoomCapCard, WorktreePortsCard,
  CallCard, VoiceSessionCard, SessionNoteCard, InstructionBatchCard, AlertCard, ServiceAlertCard, ServiceWaitCard, HelperOutcomeRow, MCPCallRow, EngineNoticeCard, QuestionNoteCard, TerminalRequestCard, ReviewResultCard,
  ShellResultRow, ShellSilenceCard, RunResultCard, JobCard, TurnCompletionRow,
}

// The post key of a registered event: its family and its event type.
export function eventKey(family?: string, type?: string): string {
  return `${family ?? ''}/${type ?? ''}`
}

// registeredEvent reports a post whose family and type the registry knows. A
// post of a sealed family with an unknown type draws the unknown event row.
export function registeredEvent(post: EventPost): boolean {
  return eventSpecs.some((spec) => spec.family === post.kind && spec.type === post.notice?.kind)
}

// eventSpec returns the registry entry of a stored post.
export function eventSpec(post: EventPost): EventSpecEntry | undefined {
  return eventSpecs.find((spec) => spec.family === post.kind && spec.type === post.notice?.kind)
}

const renderers: Record<string, EventRenderer> = {}
for (const spec of eventSpecs) {
  const component = eventComponents[spec.renderer]
  if (component) renderers[eventKey(spec.family, spec.type)] = component
}

export const eventRenderers: Readonly<Record<string, EventRenderer>> = renderers

// rendererFor returns the card of a registered event, or undefined for a type
// that has no renderer yet.
export function rendererFor(post: EventPost): EventRenderer | undefined {
  const exact = eventRenderers[eventKey(post.kind, post.notice?.kind)]
  if (exact) return exact
  // TODO: legacy/shim remove later — a post stored before its family was
  // typed has no event type; its family's card reads the stored text instead.
  // Remove when retained room history ages out.
  if (!post.notice?.kind) return familyRenderer(post.kind)
  return undefined
}

// familyRenderer is the card of a family, for a post that names no type.
function familyRenderer(family?: string): EventRenderer | undefined {
  const spec = eventSpecs.find((entry) => entry.family === family)
  return spec ? eventComponents[spec.renderer] : undefined
}

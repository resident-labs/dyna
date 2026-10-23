// Guards for JSON that arrives as unknown. A predicate returns true only
// when every property its type declares is present with the right type, or
// absent when optional. It returns the original value, so no wire data is
// lost. Nothing here fills in or changes data.
import type { ComputerApp, ComputerInfo, ComputerStream, ActionIntent } from './computer.ts'
import type { AppEvent, ListChange, ListEvent } from './event.ts'
import type { Attachment, Check, Delivery, Doing, Draft, FeedSearch, Forward, Member, Message, Post, PostDetail, PostResult, EventNotice, Seat, SendResult, Shot, Skip, SpawnPreview, SpawnResult } from './room.ts'
import type { VoicePageEvent } from './voice.ts'
import { eventSpecs, isEventPayload } from '../rooms/events.gen.ts'

export type Guard<T> = (v: unknown) => v is T

export function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

export const isString = (v: unknown): v is string => typeof v === 'string'
export const isNumber = (v: unknown): v is number => typeof v === 'number'
export const isBoolean = (v: unknown): v is boolean => typeof v === 'boolean'

export function arrayOf<T>(item: Guard<T>): Guard<T[]> {
  return (v): v is T[] => Array.isArray(v) && v.every(item)
}

const isStrings = arrayOf(isString)

// optional accepts an absent property or one that passes the guard.
export const optional = (v: unknown, guard: Guard<unknown>): boolean => v === undefined || guard(v)
// maybe is optional as a guard, so a parser checks a property once and uses the
// narrowed value.
const maybe = <T>(guard: Guard<T>): Guard<T | undefined> => (v): v is T | undefined => v === undefined || guard(v)

export const nullable = (v: unknown, guard: Guard<unknown>): boolean => v === null || guard(v)

const isStringMap = (v: unknown): v is Record<string, string> => isRecord(v) && Object.values(v).every(isString)

function isShot(v: unknown): v is Shot {
  return isRecord(v) && isNumber(v.width) && isString(v.id)
}

function isCheck(v: unknown): v is Check {
  return isRecord(v) && isString(v.name) && isString(v.status) && optional(v.detail, isString)
}

function isEventNotice(v: unknown): v is EventNotice {
  return isRecord(v) && isString(v.kind)
}

// isEventPayloadField accepts the typed payload of a sealed event. The payload
// must match the generated type of its family and type. A family or type this
// build does not know keeps its payload as a record, so the feed can show the
// unknown event row instead of dropping the post.
function isEventPayloadField(v: Record<string, unknown>): boolean {
  if (v.payload === undefined) return true
  if (!isString(v.kind) || !isRecord(v.notice) || !isString(v.notice.kind)) return false
  const family = v.kind
  const type = v.notice.kind
  if (!eventSpecs.some((spec) => spec.family === family && spec.type === type)) return isRecord(v.payload)
  return isEventPayload(family, type, v.payload)
}

function isAttachment(v: unknown): v is Attachment {
  return (
    isRecord(v) &&
    isString(v.id) &&
    isString(v.name) &&
    isString(v.mime) &&
    isNumber(v.size) &&
    isString(v.sha256) &&
    optional(v.render, isString) &&
    optional(v.title, isString) &&
    optional(v.height, isNumber) &&
    optional(v.net, isBoolean) &&
    optional(v.visual, isString) &&
    optional(v.version, isNumber) &&
    optional(v.banner, isString) &&
    optional(v.thumb, isString) &&
    optional(v.shots, arrayOf(isShot)) &&
    optional(v.checks, arrayOf(isCheck))
  )
}

const isAttachments = arrayOf(isAttachment)

function isForward(v: unknown): v is Forward {
  return (
    isRecord(v) &&
    isString(v.id) &&
    isString(v.roomId) &&
    isNumber(v.ts) &&
    isString(v.from) &&
    isString(v.text) &&
    optional(v.kind, isString) &&
    optional(v.notice, isEventNotice) &&
    optional(v.human, isBoolean) &&
    optional(v.attachments, isAttachments)
  )
}

function isPostDetail(v: unknown): v is PostDetail {
  return isRecord(v) && isString(v.kind) && isString(v.id) && optional(v.turn, isNumber)
}

// messageFields checks the properties of Message on a record.
function messageFields(v: Record<string, unknown>): boolean {
  return (
    isBoolean(v.wake) &&
    isString(v.id) &&
    isNumber(v.ts) &&
    isString(v.roomId) &&
    isString(v.from) &&
    nullable(v.to, isStrings) &&
    isString(v.text) &&
    optional(v.sessionIds, isStringMap) &&
    optional(v.kind, isString) &&
    optional(v.notice, isEventNotice) &&
    isEventPayloadField(v) &&
    optional(v.human, isBoolean) &&
    optional(v.source, isString) &&
    optional(v.spoken, isBoolean) &&
    optional(v.relayOf, isString) &&
    optional(v.via, isStrings) &&
    optional(v.replyTo, isString) &&
    optional(v.forward, isForward) &&
    optional(v.turn, isNumber) &&
    optional(v.ownerReport, isBoolean) &&
    optional(v.attachments, isAttachments)
  )
}

export function isMessage(v: unknown): v is Message {
  return isRecord(v) && messageFields(v)
}

export function isPost(v: unknown): v is Post {
  return (
    isRecord(v) &&
    messageFields(v) &&
    isBoolean(v.owner) &&
    isBoolean(v.completion) &&
    isNumber(v.fromHue) &&
    isString(v.speaker) &&
    optional(v.detail, isPostDetail) &&
    optional(v.preview, isString) &&
    optional(v.hidden, isNumber) &&
    optional(v.unread, isBoolean) &&
    optional(v.fromEmoji, isString) &&
    optional(v.fullResult, isBoolean) &&
    optional(v.resultCut, isBoolean) &&
    // TODO: legacy/shim — remove after cached feeds expire. A feed cached
    // before the feed view rename still names its views `channels`.
    optional(v.views ?? v.channels, isStrings) &&
    optional(v.pinned, isBoolean) &&
    optional(v.pinnedAt, isNumber) &&
    optional(v.pinnedBy, isString)
  )
}

export function isFeedSearch(v: unknown): v is FeedSearch {
  return isRecord(v) && isPosts(v.posts) && isBoolean(v.more)
}

function isActionIntent(v: unknown): v is ActionIntent {
  return isRecord(v) && isString(v.text) && isString(v.at)
}

function isComputerApp(v: unknown): v is ComputerApp {
  return isRecord(v) && isString(v.appId) && isString(v.title)
}

function isComputerStream(v: unknown): v is ComputerStream {
  return isRecord(v) && isBoolean(v.running) && isString(v.codec) && isString(v.encoder)
}

export function isComputerInfo(v: unknown): v is ComputerInfo {
  return (
    isRecord(v) &&
    isString(v.session) &&
    isString(v.name) &&
    isString(v.state) &&
    nullable(v.apps, arrayOf(isComputerApp)) &&
    isNumber(v.viewers) &&
    isComputerStream(v.stream) &&
    isNumber(v.rssKiB) &&
    isNumber(v.streamRssKiB) &&
    optional(v.latestStatus, isActionIntent) &&
    optional(v.room, isString) &&
    optional(v.reason, isString) &&
    optional(v.width, isNumber) &&
    optional(v.height, isNumber) &&
    optional(v.renderer, isString) &&
    optional(v.control, isString) &&
    optional(v.idleSeconds, isNumber) &&
    optional(v.startedAt, isString) &&
    optional(v.startupMs, isNumber) &&
    optional(v.shotAt, isString)
  )
}

export function isListChange(v: unknown): v is ListChange {
  return (
    isRecord(v) &&
    isString(v.op) &&
    isString(v.id) &&
    optional(v.inHead, isBoolean) &&
    optional(v.controls, isBoolean) &&
    optional(v.consolidations, isBoolean)
  )
}

// listKinds are the event kinds that name a room list. The engine also sends
// hooks and sync events; the client has no use for them, so parseAppEvent
// returns null for them as well as for malformed events.
const listKinds = ['members', 'runs', 'pins', 'goals', 'questions', 'call-requests', 'jobs', 'changes', 'prs', 'issues', 'session'] as const satisfies readonly ListEvent['kind'][]

const isPosts = arrayOf(isPost)
const isOptionalPosts = maybe(isPosts)
const isOptionalNumber = maybe(isNumber)
const isOptionalString = maybe(isString)
const isOptionalBoolean = maybe(isBoolean)
const isOptionalComputer = maybe(isComputerInfo)
const isOptionalListChange = maybe(isListChange)

const isListKind = (k: string): k is ListEvent['kind'] => listKinds.some((x) => x === k)

// parseAppEvent reads one event of the live stream. It returns null for a
// kind the client does not use and for a property of the wrong type. Absent
// optional properties stay absent.
export function parseAppEvent(raw: unknown): AppEvent | null {
  if (!isRecord(raw) || !isString(raw.kind) || !isOptionalString(raw.roomId)) return null
  const roomId = raw.roomId === undefined ? {} : { roomId: raw.roomId }
  switch (raw.kind) {
    case 'rooms':
      return { kind: 'rooms' }
    case 'notices':
      return { kind: 'notices' }
    case 'machines':
      return { kind: 'machines' }
    case 'resync':
      return { kind: 'resync' }
    case 'posts': {
      const { posts, offset } = raw
      if (!isOptionalPosts(posts) || !isOptionalNumber(offset)) return null
      return { kind: 'posts', ...roomId, ...(posts === undefined ? {} : { posts }), ...(offset === undefined ? {} : { offset }) }
    }
    case 'computer': {
      const { session, type, computer, action } = raw
      if (!isString(session) || !isString(type) || !isOptionalComputer(computer) || !isOptionalString(action)) return null
      return { kind: 'computer', session, type, ...roomId, ...(computer === undefined ? {} : { computer }), ...(action === undefined ? {} : { action }) }
    }
  }
  const { kind } = raw
  const { change } = raw
  if (!isListKind(kind) || !isOptionalListChange(change)) return null
  return { kind, ...roomId, ...(change === undefined ? {} : { change }) }
}

// parseVoicePageEvent reads one message from the page socket. It returns null
// for a type the page does not know and for a property of the wrong type.
// Absent optional properties stay absent.
export function parseVoicePageEvent(raw: unknown): VoicePageEvent | null {
  if (!isRecord(raw)) return null
  const { id, text, state, name, message, final } = raw
  if (!isOptionalString(id) || !isOptionalString(text) || !isOptionalString(state) || !isOptionalString(name) || !isOptionalString(message) || !isOptionalBoolean(final)) return null
  const withId = id === undefined ? {} : { id }
  switch (raw.type) {
    case 'state':
      return isString(state) ? { type: 'state', state } : null
    case 'audio':
      return isString(raw.audio) ? { type: 'audio', audio: raw.audio, ...withId } : null
    case 'audio_done':
      return { type: 'audio_done', ...withId }
    case 'clear':
      return { type: 'clear', ...withId }
    case 'transcript':
      return { type: 'transcript', ...(text === undefined ? {} : { text }), ...(final === undefined ? {} : { final }) }
    case 'unspoken':
      return isString(raw.post) ? { type: 'unspoken', post: raw.post } : null
    case 'tool':
      return isString(id) ? { type: 'tool', id, ...(name === undefined ? {} : { name }), ...(text === undefined ? {} : { text }), ...(state === undefined ? {} : { state }) } : null
    case 'error':
      return { type: 'error', ...(message === undefined ? {} : { message }) }
    default:
      return null
  }
}

function isDoing(v: unknown): v is Doing {
  return isRecord(v) && optional(v.text, isString) && optional(v.since, isNumber)
}

function isSeat(v: unknown): v is Seat {
  return isRecord(v) && optional(v.external, isBoolean) && optional(v.harness, isString) && optional(v.since, isString)
}

export function isMember(v: unknown): v is Member {
  return (
    isRecord(v) &&
    isBoolean(v.resumable) &&
    isString(v.name) &&
    isString(v.address) &&
    isNumber(v.depth) &&
    isNumber(v.children) &&
    isString(v.status) &&
    isString(v.state) &&
    isNumber(v.turns) &&
    isString(v.created) &&
    isString(v.lastActivity) &&
    isNumber(v.hue) &&
    optional(v.id, isString) &&
    optional(v.service, isBoolean) &&
    optional(v.role, isString) &&
    optional(v.model, isString) &&
    optional(v.transfer, isString) &&
    optional(v.profile, isString) &&
    optional(v.parent, isString) &&
    optional(v.owner, isBoolean) &&
    optional(v.ended, isString) &&
    optional(v.doing, isDoing) &&
    optional(v.closed, isBoolean) &&
    optional(v.emoji, isString) &&
    optional(v.seat, isSeat)
  )
}

// isDelivery checks notify.Delivery. Go types delivery as a plain string. The
// engine sets room, session-queued, worker-steered, root-started or failed
// today, so a new value must not make a stored post look unsent.
function isDelivery(v: unknown): v is Delivery {
  return isRecord(v) && isString(v.to) && isString(v.delivery) && optional(v.turn, isNumber) && optional(v.pointer, isBoolean) && optional(v.error, isString)
}

const isDeliveries = arrayOf(isDelivery)

export function isPostResult(v: unknown): v is PostResult {
  return (
    isRecord(v) &&
    isMessage(v.message) &&
    nullable(v.deliveries, isDeliveries) &&
    optional(v.failed, isDeliveries) &&
    optional(v.sticky, isString)
  )
}

function isSpawnResult(v: unknown): v is SpawnResult {
  return isRecord(v) && isMember(v.member) && optional(v.warning, isString)
}

// isSendResult accepts the one payload that matches kind. Go sends the other
// payload only when it is set, so a result with both is malformed.
export function isSendResult(v: unknown): v is SendResult {
  if (!isRecord(v)) return false
  if (v.kind === 'post') return isPostResult(v.post) && v.spawn === undefined
  if (v.kind === 'spawn') return isSpawnResult(v.spawn) && v.post === undefined
  return false
}

function isSkip(v: unknown): v is Skip {
  return isRecord(v) && isString(v.to) && isString(v.reason)
}

function isSpawnPreview(v: unknown): v is SpawnPreview {
  return (
    isRecord(v) &&
    isString(v.profile) &&
    isString(v.room) &&
    optional(v.harness, isString) &&
    optional(v.model, isString) &&
    optional(v.name, isString) &&
    optional(v.task, isString) &&
    optional(v.dir, isString) &&
    optional(v.error, isString)
  )
}

export function isDraft(v: unknown): v is Draft {
  return (
    isRecord(v) &&
    nullable(v.mentions, isStrings) &&
    isString(v.text) &&
    isBoolean(v.default) &&
    nullable(v.wakes, isStrings) &&
    isBoolean(v.self) &&
    optional(v.skipped, arrayOf(isSkip)) &&
    optional(v.spawn, isSpawnPreview)
  )
}

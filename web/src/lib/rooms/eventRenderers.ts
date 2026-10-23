// The web side of the system event registry. events.gen.ts is generated from
// internal/room/events.go by `DYNA_UPDATE_EVENTS=1 go test ./internal/room/
// -run Events`, so the payload types of an event and its card cannot drift.
//
// A renderer takes { post, notice, surface } and reads the typed payload only:
// no text parsing and no feed-only state. The feed draws it now. The session DM
// row is wired in PR 3 (system-events.md section 3.4.2); the surface prop and
// the DM-surface render in the test keep the card ready for it.
import type { Component } from 'svelte'
import WorktreeCard from './WorktreeCard.svelte'
import HookCard from './HookCard.svelte'
import { eventSpecs } from './events.gen.ts'
import type { EventSpecEntry } from './events.gen.ts'
import type { Message, PRNotice } from '../api/room.ts'

export type EventSurface = 'feed' | 'dm'

export interface EventProps {
  post: Message
  notice?: PRNotice
  surface?: EventSurface
}

export type EventRenderer = Component<EventProps>

// Components by the name a spec gives in its renderer field. A name with no
// component here draws the unknown event row, and the test fails.
export const eventComponents: Record<string, EventRenderer> = { WorktreeCard, HookCard }

// The post key of a registered event: its family and its event type.
export function eventKey(family?: string, type?: string): string {
  return `${family ?? ''}/${type ?? ''}`
}

// sealedEvent reports a post of a family that only the engine posts. The feed
// draws it as a card, never as a message bubble.
export function sealedEvent(post: Message): boolean {
  return eventSpecs.some((spec) => spec.family === post.kind)
}

// eventSpec returns the registry entry of a stored post.
export function eventSpec(post: Message): EventSpecEntry | undefined {
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
export function rendererFor(post: Message): EventRenderer | undefined {
  return eventRenderers[eventKey(post.kind, post.notice?.kind)]
}

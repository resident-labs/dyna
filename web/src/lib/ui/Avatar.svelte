<script lang="ts">
  import type { Avatar } from '../api/session.gen.ts'
  import { sessionStatus } from '../sessions/status.ts'
  import { initials } from './format.ts'

  interface Props {
    name?: string
    hue?: number
    size?: number
    // state is the member's session state; the dot draws it.
    state?: string
    status?: string
    // dotGap is the surface color around the status dot and room ring.
    dotGap?: string
    avatar?: Avatar
    label?: string
    here?: string
  }
  let { name = '', hue = 0, size = 40, state = '', status = '', dotGap = 'var(--main)', avatar, label = '', here = '' }: Props = $props()
</script>

<span class="avatar bg-{hue}" class:room-ring={!!avatar?.room && !!avatar.color && avatar.room !== here} class:workflow={avatar?.kind === 'workflow'} style="--s:{size}px; --gap:{dotGap}; --room:var(--room-{avatar?.color ?? 'crimson'})">
  {#if avatar?.kind === 'workflow'}
    <svg viewBox="0 0 20 20" aria-hidden="true"><path d="M6 6.5 14 10M6 13.5 14 10" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /><circle cx="5.5" cy="6" r="2.2" fill="currentColor" /><circle cx="5.5" cy="14" r="2.2" fill="currentColor" /><circle cx="14.5" cy="10" r="2.6" fill="currentColor" /></svg>
  {:else if avatar?.emoji}
    <span class="glyph" aria-hidden="true">{avatar.emoji}</span>
  {:else if !avatar || avatar.kind === 'unknown' || avatar.kind === 'owner'}
    <span class="letters">{avatar?.kind === 'owner' ? name : initials(label || name)}</span>
  {/if}
  {#if state}
    <span class="dot {state}" title={sessionStatus({ state, status })}></span>
  {/if}
</span>

<style>
  .avatar {
    position: relative;
    flex: none;
    width: var(--s);
    height: var(--s);
    border-radius: 50%;
    display: inline-grid;
    place-items: center;
    background: color-mix(in srgb, var(--h) 20%, #1d1c1f);
    color: var(--h);
  }
  .letters {
    font-weight: 600;
    font-size: calc(var(--s) * 0.36);
    letter-spacing: 0.01em;
    line-height: 1;
  }
  .room-ring { box-shadow: 0 0 0 2px var(--gap), 0 0 0 4px var(--room); }
  .workflow { background: color-mix(in srgb, var(--gold) 18%, #1d1c1f); color: var(--gold); }
  svg { width: 56%; height: 56%; }
  .glyph {
    font-family: 'Noto Color Emoji', 'Apple Color Emoji', 'Segoe UI Emoji', sans-serif;
    font-size: calc(var(--s) * 0.56);
    line-height: 1;
  }
  .dot {
    position: absolute;
    right: -2px;
    bottom: -2px;
    width: calc(var(--s) * 0.34);
    height: calc(var(--s) * 0.34);
    min-width: 10px;
    min-height: 10px;
    border-radius: 50%;
    border: 3px solid var(--gap);
    background: var(--faint);
  }
  .dot.live { background: var(--gold); }
  .dot.done { background: var(--sage); }
  .dot.error { background: var(--coral); }
  .dot.finished { background: var(--coral); }
  .dot.idle { background: #77736b; }
  .dot.closed { background: transparent; border-width: 3px; box-shadow: inset 0 0 0 2px #5e5a54; }
</style>

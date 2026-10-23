<script lang="ts">
  // One row of a session's messages view and of its thread panel: the agent's
  // avatar and name, then what the row knows. The caller decides what a tap
  // opens: a thread, a child session or a peer session.
  import Avatar from '../ui/Avatar.svelte'

  interface Props {
    name: string
    onopen: () => void
    emoji?: string
    hue?: number
    state?: string
    status?: string
    glyph?: string
    badge?: string
    note?: string
    snippet?: string
    time?: string
    queued?: boolean
    selected?: boolean
    enter?: boolean
    title?: string
  }
  let { name, onopen, emoji = '', hue = 0, state = '', status = '', glyph = '', badge = '', note = '', snippet = '', time = '', queued = false, selected = false, enter = false, title = '' }: Props = $props()
</script>

<button class="thread-chip" class:enter class:selected title={title || undefined} onclick={onopen}>
  {#if glyph}<span aria-hidden="true">{glyph}</span>{/if}
  <Avatar {name} {emoji} {hue} size={18} {state} {status} />
  <strong>{name}</strong>
  {#if badge}<b>{badge}</b>{/if}
  {#if note}<span class="note">{note}</span>{/if}
  {#if queued}<span class="queued">queued</span>{/if}
  {#if snippet}<span class="snippet">{snippet}</span>{/if}
  {#if time}<time>{time} ›</time>{/if}
</button>

<style>
  .thread-chip { display: flex; align-items: center; gap: var(--s2); min-width: 0; max-width: 100%; overflow: hidden; padding: 6px 10px; border: 1px solid var(--line-soft); border-radius: 8px; background: var(--side); font-size: var(--t-ui); color: var(--muted); text-align: left; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  .thread-chip b { color: var(--iris); white-space: nowrap; }
  .thread-chip strong { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .thread-chip .note { color: var(--faint); font-size: var(--t-cap); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .thread-chip .queued { color: var(--gold); font-size: var(--t-cap); white-space: nowrap; }
  .thread-chip time { color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .snippet { color: var(--faint); flex: 1; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; min-width: 0; }
  .thread-chip.selected { border-color: var(--iris-edge); background: var(--iris-wash); }
  @media (hover: hover) {
    .thread-chip:hover { background: var(--hover); }
  }
  .thread-chip:active { background: var(--select); }
</style>

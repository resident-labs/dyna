<script lang="ts">
  import type { Snippet } from 'svelte'
  import type { Tone } from './tone.ts'
  // One label chip. tone is a typed state (tone.ts); neutral is a plain
  // label. outline drops the fill for a settled state. dot marks something
  // running. For a status, prefer StatusPill.
  interface Props {
    tone?: Tone
    outline?: boolean
    dot?: boolean
    title?: string
    children: Snippet
  }
  let { tone = 'neutral', outline = false, dot = false, title = undefined, children }: Props = $props()
</script>
<span class="chip {tone}" class:outline {title}>{#if dot}<span class="dot" aria-hidden="true"></span>{/if}{@render children()}</span>
<style>
  .chip { display: inline-flex; align-items: center; gap: 5px; height: 18px; padding: 0 6px; border-radius: 9px; font-size: var(--t-cap); font-weight: 600; line-height: 1; white-space: nowrap; flex: 0 1 auto; min-width: 0; max-width: 100%; overflow: hidden; color: var(--muted); background: var(--raise); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease), border-color var(--motion-fast) var(--ease); }
  .warning, .live { color: var(--gold); background: rgba(230, 192, 123, 0.13); }
  .success { color: var(--sage); background: rgba(151, 201, 149, 0.13); }
  .danger { color: var(--coral); background: rgba(239, 138, 128, 0.14); }
  .info { color: var(--tone-info); background: var(--tone-info-wash); }
  .outline { background: transparent; box-shadow: inset 0 0 0 1px color-mix(in srgb, currentColor 35%, transparent); }
  .outline.neutral { color: var(--faint); box-shadow: inset 0 0 0 1px var(--line); }
  .dot { width: 6px; height: 6px; border-radius: 50%; background: var(--gold); animation: pulse 1.6s ease-in-out infinite; flex: none; }
  @keyframes pulse { 50% { opacity: 0.35; } }
</style>

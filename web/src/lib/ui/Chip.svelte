<script lang="ts">
  import type { Snippet } from 'svelte'
  // One status chip. tone is a state color: gold (live), sage (done),
  // coral (error), iris (focus), or none. outline drops the fill for a
  // settled state. dot marks something running.
  //
  // tone stays a plain string: each view names its own state colors, and the
  // class applies whatever word it gets.
  interface Props {
    tone?: string
    outline?: boolean
    dot?: boolean
    title?: string
    children: Snippet
  }
  let { tone = '', outline = false, dot = false, title = undefined, children }: Props = $props()
</script>
<span class="chip {tone}" class:outline {title}>{#if dot}<span class="dot" aria-hidden="true"></span>{/if}{@render children()}</span>
<style>
  .chip { display: inline-flex; align-items: center; gap: 5px; height: 18px; padding: 0 6px; border-radius: 9px; font-size: var(--t-cap); font-weight: 600; line-height: 1; white-space: nowrap; flex: 0 1 auto; min-width: 0; max-width: 100%; overflow: hidden; color: var(--muted); background: var(--raise); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease), border-color var(--motion-fast) var(--ease); }
  .gold { color: var(--gold); background: rgba(230, 192, 123, 0.13); }
  .sage { color: var(--sage); background: rgba(151, 201, 149, 0.13); }
  .coral { color: var(--coral); background: rgba(239, 138, 128, 0.14); }
  .iris { color: var(--iris); background: var(--iris-wash); }
  .outline { background: transparent; box-shadow: inset 0 0 0 1px color-mix(in srgb, currentColor 35%, transparent); }
  .outline:not(.gold, .sage, .coral, .iris) { color: var(--faint); box-shadow: inset 0 0 0 1px var(--line); }
  .dot { width: 6px; height: 6px; border-radius: 50%; background: var(--gold); animation: pulse 1.6s ease-in-out infinite; flex: none; }
  @keyframes pulse { 50% { opacity: 0.35; } }
</style>

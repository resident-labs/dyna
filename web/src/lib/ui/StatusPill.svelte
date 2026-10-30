<script lang="ts">
  // One status mark. Rule r16: color only a state that needs attention
  // (warning, danger) or that the owner cannot see otherwise. Never repeat a
  // control, such as "On" beside a switch; plain facts stay muted text. The
  // default neutral tone is an outline badge with no wash and no dot, so a
  // colored tone is always a choice. variant 'dot' is a dot and a muted label
  // for dense lists. live pulses its dot.
  import type { Tone } from './tone.ts'
  interface Props {
    label: string
    tone?: Tone
    value?: string
    // detail is the hover text.
    detail?: string
    dot?: boolean
    pulse?: boolean
    variant?: 'pill' | 'dot'
  }
  let { label, tone = 'neutral', value = '', detail = '', dot = false, pulse = false, variant = 'pill' }: Props = $props()
  let showDot = $derived(variant === 'dot' || (dot && tone !== 'neutral'))
</script>

<span class="status {variant} {tone}" title={detail || undefined}>{#if showDot}<span class="mark" class:pulse={pulse || tone === 'live'} aria-hidden="true"></span>{/if}{label}{#if value}<span class="num">{value}</span>{/if}</span>

<style>
  .status { display: inline-flex; align-items: center; gap: 6px; min-width: 0; max-width: 100%; white-space: nowrap; line-height: 1; }
  .pill { height: 20px; padding: 0 var(--s2); border-radius: 999px; border: 1px solid var(--tone-neutral-edge); font-size: var(--t-cap); font-weight: var(--w-medium); color: var(--muted); }
  .pill.success { border-color: var(--tone-success-edge); background: var(--tone-success-wash); color: var(--tone-success-fg); }
  .pill.warning { border-color: var(--tone-warning-edge); background: var(--tone-warning-wash); color: var(--tone-warning-fg); }
  .pill.danger { border-color: var(--tone-danger-edge); background: var(--tone-danger-wash); color: var(--tone-danger-fg); }
  .pill.info { border-color: var(--tone-info-edge); background: var(--tone-info-wash); color: var(--tone-info-fg); }
  .pill.live { border-color: var(--tone-live-edge); background: var(--tone-live-wash); color: var(--tone-live-fg); }
  .dot { font-size: var(--t-sm); color: var(--muted); }
  .mark { flex: none; width: 6px; height: 6px; border-radius: 50%; background: var(--tone-neutral); }
  .success .mark { background: var(--tone-success); }
  .warning .mark { background: var(--tone-warning); }
  .danger .mark { background: var(--tone-danger); }
  .info .mark { background: var(--tone-info); }
  .live .mark { background: var(--tone-live); }
  .pulse { animation: pulse 1.6s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: 0.35; } }
  @media (prefers-reduced-motion: reduce) { .pulse { animation: none; } }
</style>

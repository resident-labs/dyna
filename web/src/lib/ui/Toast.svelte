<script lang="ts">
  // One result of an action the user just took. The tone colors the glyph and
  // the edge; an action button is the one thing to do next. The view keeps
  // the timer, so this component only shows and reports.
  import type { Toast, ToastDetail } from './toast.ts'
  import { toneRole } from './toast.ts'

  interface Props {
    toast: Toast
    // detail is the list behind the Details button; null means no list.
    detail?: ToastDetail | null
    detailOpen?: boolean
    ondismiss: () => void
    onaction: (run: () => void) => void
    ontoggle?: () => void
  }

  let { toast, detail = null, detailOpen = false, ondismiss, onaction, ontoggle = () => {} }: Props = $props()
</script>

<div
  class="toast {toast.tone}"
  role={toneRole(toast.tone)}
  aria-live={toast.tone === 'error' ? 'assertive' : 'polite'}
>
  <span class="glyph" aria-hidden="true">
    {#if toast.tone === 'success'}
      <svg viewBox="0 0 16 16" width="16" height="16"><circle cx="8" cy="8" r="6.6" fill="none" stroke="currentColor" stroke-width="1.6" /><path d="m4.9 8.2 2.1 2.1 4.1-4.4" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" /></svg>
    {:else if toast.tone === 'error'}
      <svg viewBox="0 0 16 16" width="16" height="16"><circle cx="8" cy="8" r="6.6" fill="none" stroke="currentColor" stroke-width="1.6" /><path d="M8 4.6v4.2" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /><circle cx="8" cy="11.3" r="0.95" fill="currentColor" /></svg>
    {:else}
      <svg viewBox="0 0 16 16" width="16" height="16"><circle cx="8" cy="8" r="6.6" fill="none" stroke="currentColor" stroke-width="1.6" /><path d="M8 7.2v4.3" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /><circle cx="8" cy="4.8" r="0.95" fill="currentColor" /></svg>
    {/if}
  </span>
  <span class="line"><strong>{toast.subject}</strong>{#if toast.text}{' '}<span class="text">{toast.text}</span>{/if}</span>
  {#if detail}<button type="button" class="act" aria-expanded={detailOpen} onclick={ontoggle}>Details</button>{/if}
  {#if toast.action}
    <button type="button" class="act" onclick={() => toast.action && onaction(toast.action.run)}>{toast.action.label}</button>
  {/if}
  {#if toast.dismiss}
    <button type="button" class="close" title="Dismiss" aria-label="Dismiss this message" onclick={ondismiss}>×</button>
  {/if}
</div>

<style>
  .toast {
    position: fixed;
    left: 50%;
    bottom: 84px;
    transform: translateX(-50%);
    z-index: 70;
    display: flex;
    align-items: flex-start;
    gap: var(--s2);
    max-width: min(560px, calc(100vw - 32px));
    width: max-content;
    padding: 7px 6px 7px 10px;
    border: 1px solid var(--line-soft);
    border-radius: var(--r-sm);
    background: #121113;
    color: var(--ink-2);
    font-size: var(--t-ui);
    line-height: 1.35;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
  }
  .toast.success { border-color: color-mix(in srgb, var(--sage) 40%, transparent); }
  .toast.error { border-color: color-mix(in srgb, var(--coral) 45%, transparent); }
  .toast.info { border-color: color-mix(in srgb, var(--iris) 40%, transparent); }
  .glyph { flex: none; display: flex; margin-top: 1px; }
  .success .glyph { color: var(--sage); }
  .error .glyph { color: var(--coral); }
  .info .glyph { color: var(--iris); }
  .line { min-width: 0; overflow-wrap: break-word; }
  .line strong { color: var(--ink); font-weight: 600; }
  .line .text { color: var(--muted); }
  .act { flex: none; align-self: center; color: var(--iris); font-size: var(--t-sm); font-weight: 600; text-decoration: underline; padding: 2px 4px; transition: color var(--motion-fast) var(--ease); }
  .close { flex: none; align-self: center; width: 24px; height: 24px; display: flex; align-items: center; justify-content: center; border-radius: var(--r-xs); color: var(--faint); font-size: var(--t-title); line-height: 1; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .act:hover { color: var(--iris-hover); }
    .close:hover { color: var(--ink); background: var(--hover); }
  }
  .act:active { color: var(--iris-hover); }
  .close:active { color: var(--ink); background: var(--select); }
  @media (max-width: 760px) {
    .toast { bottom: calc(96px + env(safe-area-inset-bottom)); max-width: calc(100vw - 24px); padding-right: 4px; }
    /* A touch screen needs a real target for the close button. */
    .close { width: 36px; height: 36px; margin: -5px 0; }
    .act { min-height: 36px; }
  }
</style>

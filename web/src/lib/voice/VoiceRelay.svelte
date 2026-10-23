<script>
  // One quiet activity line in a conversation, outside any message: Voice
  // started work, Voice checked something, or someone shared a file. The
  // call window and the DM transcript show it.
  import { icons } from './icons.ts'

  // icon names an icons.js path; text is the request, quoted when present;
  // state is working, answered, stopped, running, done, failed, queued or
  // delivered. onopen, when given, adds an "open" link.
  let { icon, label, text = '', state = '', onopen = null } = $props()
</script>

<div class="line" class:failed={state === 'failed'}>
  <svg viewBox="0 0 24 24" width="12" height="12" aria-hidden="true"><path d={icons[icon]} fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" /></svg>
  <span class="what">{label}{#if text}: <q>{text}</q>{/if}</span>{#if state}<span class="state">{` · ${state}`}</span>{/if}{#if onopen}<span class="state"> · </span><button class="open" onclick={onopen}>open</button>{/if}
</div>
<style>
  .line { margin-top: 8px; padding: 0 var(--s3) 0 var(--gutter, 44px); font-size: var(--t-sm); color: var(--faint); overflow-wrap: anywhere; }
  svg { vertical-align: -1px; margin-right: 4px; }
  .what { color: var(--muted); }
  q { color: var(--ink-2); font-style: italic; }
  .failed .state { color: var(--coral); }
  .open { font: inherit; color: var(--iris); padding: 0; }
  @media (hover: hover) { .open:hover { text-decoration: underline; } }
</style>

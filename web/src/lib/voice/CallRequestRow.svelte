<script lang="ts">
  // Root's call request in the room feed. It reads like an open question:
  // one quiet line that ends in the request's state. While it rings, the
  // row offers Accept and Decline.
  import { icons } from './icons.ts'
  import type { CallRequest } from './callRequests.ts'

  let { request, onaccept, ondecline }: { request: CallRequest; onaccept: (r: CallRequest) => void; ondecline: (r: CallRequest) => void } = $props()
</script>

<div class="line">
  <svg viewBox="0 0 24 24" width="12" height="12" aria-hidden="true"><path d={icons.phone} fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" /></svg>
  <span class="what">root asked to call you: <q>{request.reason}</q></span><span class="state {request.state}">{` · ${request.state}`}</span>{#if request.state === 'ringing'}{' '}<button class="accept" onclick={() => onaccept(request)}>Accept</button>{' '}<button class="decline" onclick={() => ondecline(request)}>Decline</button>{/if}
</div>

<style>
  .line { font-size: var(--t-sm); color: var(--faint); overflow-wrap: anywhere; }
  svg { vertical-align: -1px; margin-right: 4px; color: var(--muted); }
  .what { color: var(--muted); }
  q { color: var(--ink-2); font-style: italic; }
  .state.ringing { color: var(--gold); }
  .state.joined { color: var(--sage); }
  .accept, .decline { font: inherit; padding: 0; }
  .accept { color: var(--gold); }
  .decline { color: var(--muted); }
</style>

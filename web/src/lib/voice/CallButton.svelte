<script>
  // The room header's "Call Voice" button. It shows only once voice is set
  // up (VoiceStatus.available). During this room's call it shows the call's
  // length instead; during a call elsewhere it asks to move the call here.
  import { voiceCall, clock } from './voiceCall.ts'
  import { offerCall } from './callState.js'
  import { icons } from './icons.js'

  let { room } = $props()
  let now = $state(Date.now())
  let offer = $derived(offerCall($voiceCall, room.id))

  $effect(() => {
    voiceCall.refresh()
  })
  $effect(() => {
    if (offer !== 'live') return
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })
</script>

{#if offer === 'live'}
  <span class="callbtn live" title="You are in a call with Voice"><span class="dot"></span>In call · {clock(now - $voiceCall.startedAt)}</span>
{:else if offer}
  <button class="callbtn" disabled={offer === 'calling'} title="Talk to root through Voice" onclick={() => voiceCall.call(room.id, room.name)}>
    <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d={icons.phone} fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" /></svg>
    {offer === 'calling' ? 'Calling…' : 'Call Voice'}
  </button>
{/if}

<style>
  .callbtn {
    flex: none;
    white-space: nowrap;
    display: inline-flex;
    align-items: center;
    gap: 7px;
    height: 32px;
    padding: 0 10px;
    border-radius: var(--r-xs);
    color: var(--ink-2);
    font-size: var(--t-ui);
    font-weight: 600;
    transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease);
  }
  @media (hover: hover) {
    .callbtn:hover { color: var(--ink); background: var(--hover); }
  }
  .callbtn:active { background: var(--select); }
  .callbtn:disabled { pointer-events: none; color: var(--gold); }
  .callbtn.live { color: var(--gold); font-variant-numeric: tabular-nums; }
  .dot { width: 6px; height: 6px; border-radius: 50%; background: var(--gold); animation: live 1.6s ease-in-out infinite; }
  @keyframes live { 50% { opacity: 0.6; } }
  @media (prefers-reduced-motion: reduce) { .dot { animation: none; } }
</style>

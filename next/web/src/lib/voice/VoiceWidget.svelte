<script lang="ts">
  import { onMount } from 'svelte';
  import { call } from '../client.ts';
  import { VoiceCall, type CallState } from './call.ts';
  let { room }: { room: string } = $props();
  let available = $state(false);
  let view = $state<CallState>({ status: '', active: false, muted: false, transcript: [] });
  let visible = $state(false);
  const voice = new VoiceCall(value => view = value);
  onMount(() => {
    let alive = true;
    const refresh = () => void call('accounts.list', {}).then(result => {
      if (alive) available = !!result.accounts?.some(a => a.provider === 'openai' && a.state === 'connected' && (!a.rooms?.length || a.rooms.includes(room)));
    }).catch(() => { if (alive) available = false; });
    refresh();
    window.addEventListener('focus', refresh);
    return () => { alive = false; window.removeEventListener('focus', refresh); voice.stop(); };
  });
</script>
{#if available}<button type="button" class="call" disabled={view.active} onclick={() => { visible = true; void voice.start(room); }}>Call</button>{/if}
{#if visible}
  <aside class="voice" aria-label="Voice call">
    <div class="controls"><div class="identity"><strong>Voice call</strong><span role="status">{view.active && view.muted ? `Muted · ${view.status}` : view.status}</span></div>
      {#if view.active}<button type="button" aria-pressed={view.muted} onclick={() => voice.mute()}>{view.muted ? 'Unmute' : 'Mute'}</button><button type="button" class="hangup" onclick={() => voice.stop()}>Hang up</button>
      {:else}<button type="button" onclick={() => visible = false}>Close</button>{/if}
    </div>
    <details><summary>Transcript{#if view.transcript.length}<span class="count">{view.transcript.length}</span>{/if}</summary><div class="transcript">{#each view.transcript as line}<p><strong>{line.role === 'owner' || line.role === 'user' ? 'You' : 'Voice'}:</strong> {line.text}</p>{/each}</div></details>
  </aside>
{/if}
<style>
  .call, .voice button { font: inherit; font-size: 13px; color: var(--text); background: none; border: 1px solid var(--line); border-radius: 6px; padding: 6px 9px; cursor: pointer; white-space: nowrap; }
  .call:hover, .voice button:hover { background: var(--hover); }
  button:focus-visible, summary:focus-visible { outline: 2px solid var(--focus); outline-offset: 3px; }
  .call:disabled { color: var(--muted); cursor: default; }
  .voice { position: fixed; z-index: 100; bottom: 20px; right: 20px; width: min(380px, calc(100vw - 32px)); box-sizing: border-box; padding: 12px 14px; background: var(--raised); color: var(--text); border: 1px solid var(--line); border-radius: 10px; box-shadow: 0 6px 24px var(--shadow-md); font-size: 13px; line-height: 1.4; }
  .controls { display: flex; align-items: center; gap: 7px; } .identity { flex: 1; min-width: 0; display: grid; gap: 2px; } .identity strong { font-weight: 500; } .identity span { color: var(--muted); font-size: 12px; overflow-wrap: anywhere; }
  .voice .hangup { color: var(--danger); }
  details { margin-top: 10px; padding-top: 9px; border-top: 1px solid var(--line); color: var(--muted); } summary { cursor: pointer; font-size: 12px; } .count { margin-left: 7px; font-variant-numeric: tabular-nums; } .transcript { max-height: 220px; overflow: auto; color: var(--text); } p { overflow-wrap: anywhere; margin: 10px 0 0; } p strong { color: var(--muted); font-weight: 500; }
  @media (max-width: 600px) { .voice { bottom: 0; right: 0; width: 100%; padding-bottom: max(14px, env(safe-area-inset-bottom)); border-radius: 10px 10px 0 0; } }
</style>

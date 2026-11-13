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
    <div class="controls"><span role="status">{view.status}</span>
      {#if view.active}<button type="button" aria-pressed={view.muted} onclick={() => voice.mute()}>{view.muted ? 'Unmute' : 'Mute'}</button><button type="button" onclick={() => voice.stop()}>Hang up</button>
      {:else}<button type="button" onclick={() => visible = false}>Close</button>{/if}
    </div>
    <details><summary>Transcript</summary><div class="transcript">{#each view.transcript as line}<p><strong>{line.role === 'user' ? 'You' : 'Voice'}:</strong> {line.text}</p>{/each}</div></details>
  </aside>
{/if}
<style>
  .call, .voice button { font: inherit; color: inherit; background: var(--raised); border: 1px solid var(--line); border-radius: 6px; padding: 6px 10px; cursor: pointer; }
  .voice { position: fixed; z-index: 100; bottom: 20px; right: 20px; width: min(380px, calc(100vw - 32px)); box-sizing: border-box; padding: 14px; background: var(--raised); color: var(--text); border: 1px solid var(--line); border-radius: 12px; box-shadow: 0 4px 24px var(--shadow-md); }
  .controls { display: flex; align-items: center; gap: 8px; } .controls span { flex: 1; } details { margin-top: 12px; } summary { cursor: pointer; } .transcript { max-height: 240px; overflow: auto; } p { overflow-wrap: anywhere; }
  @media (max-width: 600px) { .voice { bottom: env(safe-area-inset-bottom, 0px); right: 0; width: 100%; border-radius: 12px 12px 0 0; } }
</style>

<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { call } from '../client.ts';
  import { voice, voiceCall, startCall } from './store.svelte.ts';
  let { room, name = room, composer }: { room: string; name?: string; composer?: HTMLElement } = $props();
  let available = $state(false), now = $state(Date.now()), bottom = $state(0);
  let refreshVersion = $state(0);
  let expanded = $state(false), offer = $state(false);
  let transcript: HTMLDivElement | undefined = $state();
  const view = $derived(voice.view);
  const other = $derived(view.active && voice.room !== room);
  const label = $derived(!view.active ? 'Ended' : view.muted ? 'Muted' : view.status.startsWith('Connecting') || view.status.startsWith('Opening') ? 'Connecting' : view.status);
  const elapsed = $derived(Math.max(0, Math.floor(((voice.ended || now) - voice.started) / 1000)));
  const duration = $derived(`${Math.floor(elapsed / 60)}:${String(elapsed % 60).padStart(2, '0')}`);
  $effect(() => {
    const element = composer;
    const measure = () => bottom = element ? Math.max(0, window.innerHeight - element.getBoundingClientRect().top + 8) : 12;
    measure();
    const observer = new ResizeObserver(measure); if (element) observer.observe(element);
    window.addEventListener('resize', measure); window.visualViewport?.addEventListener('resize', measure);
    return () => { observer.disconnect(); window.removeEventListener('resize', measure); window.visualViewport?.removeEventListener('resize', measure); };
  });
  $effect(() => { view.transcript.length; if (expanded) void tick().then(() => { if (transcript) transcript.scrollTop = transcript.scrollHeight; }); });
  function begin() { expanded = false; startCall(room, name); }
  $effect(() => {
    const target = room; refreshVersion;
    let alive = true; available = false;
    void call('accounts.list', {}).then(result => {
      if (alive) available = !!result.accounts?.some(a => a.provider === 'openai' && a.state === 'connected' && (!a.rooms?.length || a.rooms.includes(target)));
    }).catch(() => { if (alive) available = false; });
    return () => { alive = false; };
  });
  onMount(() => {
    const refresh = () => refreshVersion++;
    const timer = setInterval(() => now = Date.now(), 1000);
    window.addEventListener('focus', refresh);
    return () => { clearInterval(timer); window.removeEventListener('focus', refresh); };
  });
</script>
<svelte:window onkeydown={event => { if (event.key === 'Escape') { expanded = false; offer = false; } }} />
{#if available}<button type="button" class="call" aria-label={other ? `Call active in ${voice.name}` : 'Call'} onclick={() => { if (other) offer = !offer; else if (view.active) voice.visible = !voice.visible; else begin(); }}>{view.active ? 'In call' : 'Call'}</button>{/if}
{#if offer && other}<div class="offer"><span>Call active in {voice.name}.</span> <button type="button" onclick={() => { voiceCall.stop(); offer = false; begin(); }}>End it and call here</button><button type="button" aria-label="Dismiss call offer" onclick={() => offer = false}>×</button></div>{/if}
{#if voice.visible}
  <aside class="voice" aria-label="Voice call" style={`--dock-bottom:${bottom}px`}>
    <div class="controls"><div class="identity"><strong><span class="dot" class:listening={label === 'Listening'} class:speaking={label === 'Speaking'} class:working={label === 'Working'} class:failed={!view.active && view.status !== 'Call ended'} style={`--level:${view.level ?? 0}`}></span>{voice.name}<span class="elapsed">· {duration}</span></strong><span role="status" aria-live="polite">{label}{!view.active && view.status !== 'Call ended' ? ` · ${view.status}` : ''}</span></div>
      {#if view.active}<button type="button" aria-pressed={view.muted} onclick={() => voiceCall.mute()}>{view.muted ? 'Unmute' : 'Mute'}</button><button type="button" class="hangup" onclick={() => voiceCall.stop()}>Hang up</button>
      {:else}<button type="button" onclick={() => { expanded = false; startCall(voice.room, voice.name); }}>Call again</button>{/if}
      <button type="button" class="close" aria-label="Hide voice call" onclick={() => voice.visible = false}>×</button>
    </div>
    <details bind:open={expanded}><summary>Transcript</summary><div class="transcript" bind:this={transcript}>{#each view.transcript as line}<p><strong>{line.role === 'owner' || line.role === 'user' ? 'You' : 'Voice'}:</strong> {line.text}</p>{/each}</div></details>
  </aside>
{/if}
<style>
  .call, .voice button, .offer button { font: inherit; font-size: 13px; color: var(--text); background: none; border: 1px solid var(--line); border-radius: 6px; padding: 6px 9px; cursor: pointer; white-space: nowrap; }
  button:hover { background: var(--hover); } button:focus-visible, summary:focus-visible { outline: 2px solid var(--focus); outline-offset: 3px; }
  .voice, .offer { position: fixed; z-index: 100; right: 20px; box-sizing: border-box; padding: 12px 14px; background: var(--raised); color: var(--text); border: 1px solid var(--line); border-radius: 10px; box-shadow: 0 6px 24px var(--shadow-md); font-size: 13px; line-height: 1.4; }
  .voice { bottom: 20px; width: min(420px, calc(100vw - 32px)); } .offer { top: 56px; max-width: calc(100vw - 24px); display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
  .controls { display: flex; align-items: center; gap: 7px; } .identity { flex: 1; min-width: 0; display: grid; gap: 3px; } .identity strong { display: flex; align-items: center; gap: 5px; font-weight: 500; overflow-wrap: anywhere; } .identity > span, .elapsed { color: var(--muted); font-size: 12px; } .elapsed { white-space: nowrap; font-weight: 400; }
  .dot { flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--muted); } .dot.listening { background: var(--focus); transform: scale(calc(1 + var(--level) * .5)); transition: transform 80ms; } .dot.speaking { background: var(--focus); animation: pulse 2s ease-in-out infinite; } .dot.working { opacity: .5; } .dot.failed { background: var(--danger); }
  @keyframes pulse { 50% { opacity: .4; } } @media (prefers-reduced-motion: reduce) { .dot { animation: none !important; transition: none !important; transform: none !important; } }
  .voice .hangup { color: var(--danger); } .voice .close { border: none; padding: 5px; color: var(--muted); }
  details { margin-top: 10px; padding-top: 9px; border-top: 1px solid var(--line); color: var(--muted); } summary { cursor: pointer; font-size: 12px; } .transcript { max-height: 180px; overflow: auto; color: var(--text); } p { overflow-wrap: anywhere; margin: 10px 0 0; } p strong { color: var(--muted); font-weight: 500; }
  @media (max-width: 600px) { .voice { bottom: var(--dock-bottom); right: 0; width: 100%; padding: 10px 12px; } .voice button { min-height: 44px; min-width: 44px; padding: 6px; } .controls { gap: 4px; } .identity strong { font-size: 12px; gap: 4px; } .elapsed { font-size: 11px; } }
</style>

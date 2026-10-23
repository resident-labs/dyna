<script>
  // The Computer tab of a session. It opens the stream at once: no "Watch
  // live" step. While it connects, the viewer shows the last picture and
  // fades to the stream. The stream closes when the tab is left, the page
  // is hidden, or the computer stops. A computer with no window yet gets a
  // calm empty state and no stream.
  import { onMount } from 'svelte'
  import ComputerViewer from './ComputerViewer.svelte'
  import ComputerShot from './ComputerShot.svelte'
  import { streamWanted, loadPreview, savePreview } from './preview.ts'
  import { stateText } from './computers.ts'

  let { c, room, feed = [], chat = null } = $props()
  let pageVisible = $state(typeof document === 'undefined' || document.visibilityState !== 'hidden')
  onMount(() => {
    const f = () => (pageVisible = document.visibilityState !== 'hidden')
    document.addEventListener('visibilitychange', f)
    return () => document.removeEventListener('visibilitychange', f)
  })
  // The tab is open whenever this component exists.
  const wanted = $derived(streamWanted({ tab: 'computer', c, pageVisible }))
  // svelte-ignore state_referenced_locally
  let hiddenPreview = $state(loadPreview(c.session).hidden !== null)
  function showPreview() {
    savePreview(c.session, { ...loadPreview(c.session), hidden: null })
    hiddenPreview = false
  }
  const empty = $derived(c.state === 'none' || c.state === 'idle')
</script>

{#if wanted}
  <ComputerViewer {c} {room} {feed} {chat} />
{:else}
  <div class="pane">
    {#if empty}
      <div class="empty">
        <b>No window yet.</b>
        <span>It starts when this agent opens an app.</span>
      </div>
    {:else}
      <div class="still">
        <ComputerShot session={c.session} shotAt={c.shotAt} phase={c.state} alt="Latest picture of the computer" />
        <span class="badge">{c.state === 'starting' ? 'Starting…' : pageVisible ? stateText(c) : 'Paused while the page is hidden'}</span>
      </div>
    {/if}
    {#if hiddenPreview}<button class="link" onclick={showPreview}>Show the floating preview again</button>{/if}
  </div>
{/if}

<style>
  .pane { flex: 1; min-height: 0; padding: 12px 16px; display: flex; flex-direction: column; gap: 10px; }
  .empty { flex: 1; display: grid; place-content: center; text-align: center; gap: 4px; color: var(--muted); font-size: 14px; }
  .empty b { color: var(--ink-2); font-weight: 500; }
  .still { position: relative; flex: 1; min-height: 0; border: 2px solid var(--line); border-radius: 8px; background: #000; overflow: hidden; }
  .badge { position: absolute; left: 10px; top: 10px; background: rgba(20, 19, 22, 0.82); color: var(--ink-2); font-size: 12px; padding: 3px 10px; border-radius: 12px; }
  .link { align-self: flex-start; font-size: 12px; color: var(--muted); text-decoration: underline; }
</style>

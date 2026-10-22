<script>
  // The viewer alone, in its own browser window (web only). It opens from
  // the viewer's Pop out button at #pop=<room>:<session>.
  import { onMount } from 'svelte'
  import { api, onEvent } from '../../api.js'
  import ComputerViewer from './ComputerViewer.svelte'
  import { applyEvent, pushAction } from './computers.js'
  let { room, session } = $props()
  let c = $state(null)
  let feed = $state({})
  let error = $state('')

  async function load() {
    try {
      const [st, members] = await Promise.all([api.computer(session), api.members(room, false).catch(() => [])])
      c = { ...st, session, name: members?.find((m) => m.id === session)?.name ?? '' }
    } catch (e) {
      error = String(e)
    }
  }
  onMount(() => {
    load()
    return onEvent((ev) => {
      if (ev.kind !== 'computer' || ev.session !== session) return
      if (ev.action) feed = pushAction(feed, session, ev.action)
      if (c && ev.computer) c = { ...ev.computer, session, name: c.name }
    })
  })
  document.title = 'Computer · Dyna'
</script>

<main>
  {#if error}<p class="err">{error}</p>
  {:else if c}<ComputerViewer {c} {room} feed={feed[session] ?? []} popout />
  {:else}<p class="note">Loading…</p>{/if}
</main>

<style>
  main { height: 100%; display: flex; flex-direction: column; background: var(--main); }
  .err { color: var(--coral); padding: 16px; }
  .note { color: var(--muted); padding: 16px; }
</style>

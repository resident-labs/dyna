<script>
  import { openExternal, cardURL } from '../../api.js'
  import { sandbox, guard, onLoad, readCardMessage, frameKey, forKey } from '../ui/rich.js'
  // One HTML card frame with its link guard and its message channel. See
  // rich.js for the rules.
  let { roomId, att, height = '100%', onheight } = $props()
  let attId = $derived(att.id)
  let title = $derived(att.title || att.name)
  let g = $state(guard())
  // The guard of the document now in the frame.
  let current = $derived(forKey(g, frameKey(att)))
  let frame = $state()
  let url = $state('')
  let urlError = $state('')
  const NOTE = 'This card tried to open a link by navigating. Cards open links with dyna:open after a click.'

  // The URL comes from api.cardURL: a fixed path in the desktop, a URL with
  // a ten-minute ticket on the web. A restore or a reload asks again, so a
  // card never reloads a dead ticket.
  async function fetchURL() {
    try {
      const u = await cardURL(roomId, attId)
      urlError = ''
      return u
    } catch (e) {
      urlError = String(e)
      return ''
    }
  }
  $effect(() => {
    roomId, attId
    fetchURL().then((u) => (url = u))
  })

  async function loaded() {
    const [next, act] = onLoad(current)
    g = next
    // Setting src, even to the same URL, navigates the frame back.
    if (act === 'restore' && frame) {
      const u = await fetchURL()
      if (u && frame) {
        url = u
        frame.src = u
      }
    }
  }

  async function reload() {
    url = await fetchURL()
    g = guard()
  }

  // Only this card's own frame may talk to it: the source must be its
  // window, and an opaque-origin card always reports origin "null".
  // A height applies only when it changes, at most five times a second, so
  // a flood cannot thrash the feed layout. A link opens only while the card
  // has focus and the owner has just clicked or typed (transient user
  // activation), so a card cannot open pages on load or on a timer. One
  // open per card every two seconds: one click, one link.
  let lastHeight = 0
  let heightAt = 0
  let heightTimer
  let openAt = 0
  function message(e) {
    if (!frame || e.source !== frame.contentWindow || e.origin !== 'null') return
    const m = readCardMessage(e.data)
    const now = Date.now()
    if (m?.type === 'height' && m.height !== lastHeight) {
      // Apply at most five times a second, but always apply the last one.
      clearTimeout(heightTimer)
      const apply = () => {
        lastHeight = m.height
        heightAt = Date.now()
        onheight?.(m.height)
      }
      if (now - heightAt >= 200) apply()
      else heightTimer = setTimeout(apply, 200 - (now - heightAt))
    }
    if (m?.type === 'open' && document.activeElement === frame && navigator.userActivation?.isActive === true) {
      if (now - openAt < 2000) return
      openAt = now
      openExternal(m.url)
    }
  }
</script>

<svelte:window onmessage={message} />

{#if current.state === 'stopped'}
  <div class="stopped" style="height: {height}">
    <p>{NOTE}</p>
    <button onclick={reload}>Reload card</button>
  </div>
{:else if urlError}
  <div class="stopped" style="height: {height}">
    <p>The card did not load: {urlError}</p>
    <button onclick={reload}>Reload card</button>
  </div>
{:else if url}
  <!-- Keep the frame in the viewer dialog's Tab order for keyboard scrolling. -->
  <!-- A new frame when the card's policy changes: a loaded frame keeps the CSP it loaded with. -->
  {#key frameKey(att)}<iframe bind:this={frame} src={url} sandbox={sandbox()} referrerpolicy="no-referrer" loading="lazy" tabindex="0" {title} style="height: {height}" onload={loaded}></iframe>{/key}
  {#if current.restores}<p class="note">{NOTE}</p>{/if}
{:else}
  <!-- Holds the card's height while its URL resolves, so the feed does not
       jump after it scrolls to the newest post. -->
  <div class="frame-wait" style="height: {height}" aria-hidden="true"></div>
{/if}

<style>
  /* The frame keeps its own scrollbar: a declared height is width-blind. */
  iframe { display: block; width: 100%; border: 0; background: #18171a; flex: 1; min-height: 0; }
  .frame-wait { background: #18171a; flex: 1; min-height: 0; }
  .note { margin: 0; padding: 6px 10px; font-size: 12px; color: var(--gold); border-top: 1px solid var(--line-soft); }
  .stopped { display: grid; place-content: center; gap: 10px; text-align: center; color: var(--muted); font-size: 13px; padding: 0 16px; flex: 1; }
  .stopped button { justify-self: center; color: var(--ink); background: var(--raise); padding: 5px 12px; border-radius: 6px; }
</style>

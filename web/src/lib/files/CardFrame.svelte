<script lang="ts">
  import { api, openExternal, cardURL } from '../../api.ts'
  import { errorText } from '../errorText.ts'
  import { questionView } from '../app/questions.svelte.ts'
  import { sandbox, guard, onLoad, readCardMessage, frameKey, forKey } from '../ui/rich.ts'
  import type { CardGuard } from '../ui/rich.ts'
  import { replyDraft, routeSubmit } from './cardSubmit.ts'
  import type { CardPost, SubmitRoute } from './cardSubmit.ts'
  import type { Attachment } from '../api/room.ts'

  interface Props {
    roomId: string
    att: Attachment
    // The post that carries the card. It decides where dyna:submit goes.
    post?: CardPost
    height?: string
    onheight?: (height: number) => void
  }
  // One HTML card frame with its link guard and its message channel. See
  // rich.ts for the rules.
  let { roomId, att, post, height = '100%', onheight }: Props = $props()
  let attId = $derived(att.id)
  let title = $derived(att.title || att.name)
  let g = $state<CardGuard>(guard())
  // The guard of the document now in the frame.
  let current = $derived(forKey(g, frameKey(att)))
  let frame = $state<HTMLIFrameElement>()
  let url = $state('')
  let urlError = $state('')
  const NOTE = 'This card tried to open a link by navigating. Cards open links with dyna:open after a click.'

  // The URL comes from api.cardURL: a fixed path in the desktop, a URL with
  // a ten-minute ticket on the web. A restore or a reload asks again, so a
  // card never reloads a dead ticket.
  async function fetchURL(): Promise<string> {
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
    roomId
    attId
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
  // open per card every two seconds: one click, one link. A dyna:submit
  // follows the same rules, with its own two-second limit.
  let lastHeight = 0
  let heightAt = 0
  let heightTimer: ReturnType<typeof setTimeout> | undefined
  let openAt = 0
  let submitAt = 0
  // The submit that waits for the owner's Send click.
  let pending = $state<SubmitRoute | null>(null)
  let sending = $state(false)
  function message(e: MessageEvent) {
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
    // A submit passes the same gate as an open, then waits for the owner to
    // confirm it with the app's own Send button, outside the frame. A card
    // with no post (a file preview) has nowhere to send, so it gets an error.
    if (m?.type === 'submit' && document.activeElement === frame && navigator.userActivation?.isActive === true) {
      if (now - submitAt < 2000) return
      submitAt = now
      propose(m.text, m.choice)
    }
  }

  // propose shows the strip for one submit. A newer submit replaces a pending
  // one, and the card hears that the old one was replaced.
  function propose(text: string, choice?: string) {
    if (!post) return reply({ type: 'dyna:submitted', ok: false, error: 'no-post' })
    const route = routeSubmit(post, questionView.questions, { text, choice })
    if (route.kind === 'refuse') return reply({ type: 'dyna:submitted', ok: false, error: route.error })
    if (pending) reply({ type: 'dyna:submitted', ok: false, error: 'replaced' })
    pending = route
  }

  function cancel() {
    pending = null
    reply({ type: 'dyna:submitted', ok: false, error: 'cancelled' })
  }

  // Only this click sends. It uses the same calls as the app's question
  // buttons and composer, marked as written by a card.
  async function confirm() {
    const route = pending
    if (!route || route.kind === 'refuse' || sending) return
    sending = true
    try {
      if (route.kind === 'answer') await questionView.answerFromCard(route.question, route.choice, route.text)
      else await api.sendFromCard(roomId, replyDraft(route), route.replyTo, route.conversation)
      reply({ type: 'dyna:submitted', ok: true })
    } catch (err) {
      reply({ type: 'dyna:submitted', ok: false, error: errorText(err) })
    } finally {
      sending = false
      pending = null
    }
  }

  // The card's origin is opaque, so the only target origin that reaches it is "*".
  function reply(message: { type: 'dyna:submitted'; ok: boolean; error?: string }) {
    frame?.contentWindow?.postMessage(message, '*')
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
  {#if pending && pending.kind !== 'refuse'}
    <div class="confirm" role="group" aria-label="Confirm what the card sends">
      <p class="what">{pending.kind === 'answer' ? 'This card wants to answer the question with' : 'This card wants to reply to its author with'}</p>
      <pre class="text">{pending.kind === 'answer' ? pending.choice || pending.text : pending.text}</pre>
      <div class="buttons"><button class="send" disabled={sending} onclick={confirm}>Send</button><button disabled={sending} onclick={cancel}>Cancel</button></div>
    </div>
  {/if}
{:else}
  <!-- Holds the card's height while its URL resolves, so the feed does not
       jump after it scrolls to the newest post. -->
  <div class="frame-wait" style="height: {height}" aria-hidden="true"></div>
{/if}

<style>
  /* The frame keeps its own scrollbar: a declared height is width-blind. */
  iframe { display: block; width: 100%; border: 0; background: #18171a; flex: 1; min-height: 0; }
  .frame-wait { background: #18171a; flex: 1; min-height: 0; }
  .confirm { padding: 8px 10px; border-top: 1px solid var(--line-soft); background: var(--raise); display: grid; gap: 6px; flex: none; }
  .confirm .what { margin: 0; font-size: 12px; color: var(--muted); }
  .confirm .text { margin: 0; max-height: 120px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; font: 12.5px/1.5 var(--mono); color: var(--ink); }
  .buttons { display: flex; gap: 8px; }
  .buttons button { color: var(--ink); background: var(--select); padding: 4px 12px; border-radius: 6px; font-size: 12px; }
  .buttons .send { color: var(--iris); background: var(--iris-wash); }
  .buttons button:disabled { opacity: 0.5; }
  .note { margin: 0; padding: 6px 10px; font-size: 12px; color: var(--gold); border-top: 1px solid var(--line-soft); }
  .stopped { display: grid; place-content: center; gap: 10px; text-align: center; color: var(--muted); font-size: 13px; padding: 0 16px; flex: 1; }
  .stopped button { justify-self: center; color: var(--ink); background: var(--raise); padding: 5px 12px; border-radius: 6px; }
</style>

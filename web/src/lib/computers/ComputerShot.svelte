<script>
  // The agent's latest picture. It is a plain image of last.png, never a
  // stream. A new picture loads off screen first, so the tile never flickers.
  // With refresh set, a running computer asks for a fresh picture that often
  // while the page is visible; the server limits real captures to one per 5 s.
  import { api } from '../../api.ts'
  import { freshDue } from './preview.ts'
  // minGap: the least time between two loads from shotAt changes (ms).
  let { session, shotAt = null, phase = 'none', refresh = 0, alt = '', minGap = 0, children } = $props()
  let src = $state('')
  let failed = $state(false)
  let seq = 0

  function load(fresh) {
    const mine = ++seq
    const url = `${api.computerShotURL(session, fresh)}${fresh ? '&' : '?'}t=${Date.now()}`
    const img = new Image()
    img.onload = () => { if (mine === seq) { src = url; failed = false } }
    img.onerror = () => { if (mine === seq && !src) failed = true }
    img.src = url
  }

  // A new picture time from the daemon means a new file.
  let lastLoad = 0
  let timer = 0
  $effect(() => {
    shotAt
    // No picture time means no file yet: do not ask, so the console stays quiet.
    if (phase === 'none' || !shotAt) return
    const wait = lastLoad + minGap - Date.now()
    clearTimeout(timer)
    if (wait <= 0) { lastLoad = Date.now(); load(false) }
    else timer = setTimeout(() => { lastLoad = Date.now(); load(false) }, wait)
    return () => clearTimeout(timer)
  })
  // Ask the daemon for a fresh picture while this one is shown: at most once
  // per `refresh` ms, only while the page is visible, and at once when the
  // page comes back.
  let lastAsk = 0
  $effect(() => {
    if (!refresh || phase !== 'running') return
    const ask = () => {
      const now = Date.now()
      if (!freshDue({ phase, visible: document.visibilityState === 'visible', last: lastAsk, now, every: refresh - 50 })) return
      lastAsk = now
      load(true)
    }
    ask()
    const t = setInterval(ask, refresh)
    document.addEventListener('visibilitychange', ask)
    return () => { clearInterval(t); document.removeEventListener('visibilitychange', ask) }
  })
</script>

{#if src}
  <img {src} {alt} class:dim={phase !== 'running'} draggable="false" />
{:else if phase === 'none'}
  {@render children?.()}
{:else}
  <div class="none">{failed ? 'No picture yet' : ''}</div>
{/if}

<style>
  img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: contain; background: #0f0f11; }
  img.dim { opacity: 0.45; filter: grayscale(0.5); }
  .none { position: absolute; inset: 0; display: grid; place-items: center; color: var(--faint); font-size: 12px; }
</style>

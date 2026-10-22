<script>
  // The floating computer in a session's Messages view: variant A, the glass
  // card. The picture comes first. The caption sits on a dark fade at the
  // bottom: a gold dot, the app name, the window title and the
  // picture age. Minimize and hide show on hover. The minimized form is
  // variant C: a pill, "lets-work is in Chromium", that lifts the picture on
  // hover. It shows the latest screenshot only and never opens a stream.
  // A click opens the Computer tab. A drag moves it to the nearest corner of
  // the message area, and the corner is remembered per session.
  import ComputerShot from './ComputerShot.svelte'
  import { clock } from './clock.svelte.js'
  import { ageText, appName } from './computers.js'
  import { loadPreview, savePreview, snapCorner } from './preview.js'

  let { c, session, name = '', onopen } = $props()
  // The preview is keyed by session in the app, so the first value is the right one.
  // svelte-ignore state_referenced_locally
  let prefs = $state(loadPreview(session))
  let box = $state()
  let drag = $state(null) // {x, y} while dragging, in area pixels
  const app = $derived(appName(c.apps?.[0]?.appId) || (c.state === 'starting' ? 'Starting' : 'Computer'))
  const title = $derived(c.apps?.[0]?.title ?? '')

  function save(next) {
    prefs = next
    savePreview(session, next)
  }
  // A hide lasts only while the computer keeps its state.
  $effect(() => { if (prefs.hidden && prefs.hidden.state !== c.state) save({ ...prefs, hidden: null }) })

  function down(e) {
    if (e.button !== 0 || e.target.closest('button')) return
    const host = box.offsetParent.getBoundingClientRect()
    const r = box.getBoundingClientRect()
    const dx = e.clientX - r.left, dy = e.clientY - r.top
    let moved = false
    const move = (ev) => {
      moved = moved || Math.abs(ev.clientX - e.clientX) + Math.abs(ev.clientY - e.clientY) > 4
      if (moved) drag = { x: ev.clientX - host.left - dx, y: ev.clientY - host.top - dy }
    }
    const up = (ev) => {
      removeEventListener('pointermove', move)
      removeEventListener('pointerup', up)
      if (moved) {
        const corner = snapCorner(ev.clientX - host.left, ev.clientY - host.top, host.width, host.height)
        drag = null
        save({ ...prefs, corner })
      } else onopen()
    }
    addEventListener('pointermove', move)
    addEventListener('pointerup', up)
  }
  const key = (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onopen() } }
</script>

{#if prefs.hidden?.state !== c.state}
  <div class="pv {prefs.corner}" class:dragging={!!drag} bind:this={box} style:left={drag ? `${drag.x}px` : null} style:top={drag ? `${drag.y}px` : null} style:right={drag ? 'auto' : null} style:bottom={drag ? 'auto' : null} role="region" aria-label="Computer preview">
    {#if prefs.minimized}
      <div class="peek">
        <div class="lift"><div class="pic"><ComputerShot {session} shotAt={c.shotAt} phase={c.state} minGap={5000} refresh={5000} alt="Latest picture of the computer" /></div></div>
        <div class="pill" onpointerdown={down} role="button" tabindex="0" onkeydown={key}>
          <span class="dot" class:live={c.state === 'running'}></span>
          <i>{name || 'The agent'}</i>
          <span class="in">is in {app}</span>
          <button class="ic" onclick={() => save({ ...prefs, minimized: false })} aria-label="Expand the preview" title="Expand">▴</button>
        </div>
      </div>
    {:else}
      <div class="card" onpointerdown={down} role="button" tabindex="0" onkeydown={key}>
        <div class="pic"><ComputerShot {session} shotAt={c.shotAt} phase={c.state} minGap={5000} refresh={5000} alt="Latest picture of the computer" /></div>
        {#if c.state === 'starting'}<div class="starting">Starting…</div>{/if}
        <div class="cap">
          <span class="dot" class:live={c.state === 'running'}></span>
          <i>{app}</i>
          <span class="host">{title}</span>
          {#if c.shotAt}<span class="age">{ageText(c.shotAt, clock.now)}</span>{/if}
        </div>
        <div class="ctl">
          <button class="ic" onclick={() => save({ ...prefs, minimized: true })} aria-label="Minimize the preview" title="Minimize">–</button>
          <button class="ic" onclick={() => save({ ...prefs, hidden: { state: c.state } })} aria-label="Hide the preview" title="Hide until the computer changes">✕</button>
        </div>
      </div>
    {/if}
  </div>
{/if}

<style>
  .pv { position: absolute; z-index: 5; transition: top var(--motion-fast), left var(--motion-fast); }
  .pv.dragging { transition: none; cursor: grabbing; }
  /* --shift keeps the card clear of the threads panel when it is open. */
  .br { right: calc(18px + var(--shift, 0px)); bottom: 118px; }
  .bl { left: 18px; bottom: 118px; }
  .tr { right: calc(18px + var(--shift, 0px)); top: 14px; }
  .tl { left: 18px; top: 14px; }
  i { font-style: normal; font-weight: 500; }
  .card { position: relative; width: 300px; border-radius: var(--r-lg); overflow: hidden; background: #0d0d0f; border: 1px solid var(--hair); box-shadow: 0 14px 40px rgba(0, 0, 0, 0.5); cursor: pointer; user-select: none; }
  .pic { position: relative; aspect-ratio: 16 / 10; background: #0d0d0f; }
  .cap { position: absolute; left: 0; right: 0; bottom: 0; display: flex; align-items: baseline; gap: 7px; padding: 22px 12px 9px; background: linear-gradient(to top, rgba(8, 8, 10, 0.92), rgba(8, 8, 10, 0)); font-size: 11.5px; color: var(--ink-2); min-width: 0; }
  .cap i { font-size: 13px; color: var(--ink); flex: none; }
  .cap .dot { align-self: center; }
  .host { color: var(--muted); min-width: 0; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .age { font: 10.5px var(--mono); color: var(--faint); flex: none; }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--faint); flex: none; }
  .dot.live { background: var(--gold); box-shadow: 0 0 0 3px rgba(230, 192, 123, 0.16); }
  .ctl { position: absolute; top: 8px; right: 8px; display: flex; gap: 4px; opacity: 0; transition: opacity var(--motion-fast); }
  .card:focus-within .ctl { opacity: 1; }
  .card:hover .ctl { opacity: 1; }
  @media (hover: none) { .ctl { opacity: 1; } }
  .ic { width: 24px; height: 24px; border-radius: 12px; background: rgba(20, 19, 22, 0.72); color: var(--ink-2); font-size: 12px; line-height: 1; display: grid; place-items: center; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .ic:hover { background: var(--raise-hover); color: var(--ink); }
  }
  .ic:active { background: var(--select); }
  .starting { position: absolute; inset: 0; display: grid; place-items: center; background: rgba(0, 0, 0, 0.5); color: var(--ink-2); font-size: 13px; }
  /* Variant C: the pill. */
  .peek { position: relative; display: flex; flex-direction: column; align-items: flex-end; }
  .pill { display: inline-flex; align-items: center; gap: 8px; padding: 6px 8px 6px 13px; border-radius: 18px; background: var(--raise); border: 1px solid var(--hair-strong); box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4); font-size: 12.5px; color: var(--ink-2); cursor: pointer; user-select: none; white-space: nowrap; }
  .pill i { font-size: 13.5px; color: var(--ink); }
  .pill .in { color: var(--muted); }
  .lift { position: absolute; bottom: 38px; right: 0; width: 260px; border-radius: var(--r-sm); overflow: hidden; border: 1px solid var(--hair); box-shadow: 0 14px 40px rgba(0, 0, 0, 0.5); opacity: 0; transform: translateY(6px); pointer-events: none; transition: opacity var(--motion), transform var(--motion); }
  .peek:hover .lift { opacity: 1; transform: none; }
  .tr .lift, .tl .lift { bottom: auto; top: 38px; transform: translateY(-6px); }
  .tl .lift { left: 0; right: auto; }
  .bl .peek, .tl .peek { align-items: flex-start; }
  .bl .lift { left: 0; right: auto; }
</style>

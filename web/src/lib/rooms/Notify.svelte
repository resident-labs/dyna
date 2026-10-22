<script>
  import { pushSupported, pushState, pushOn, pushOff, pushNames, pushTest } from '../settings/link.ts'
  // The bell in the sidebar footer and its small panel: Web Push on or off,
  // whether a notification may show room and sender names, and a test.
  let open = $state(false)
  let on = $state(false)
  let names = $state(false)
  let busy = $state(false)
  let note = $state('')
  const supported = pushSupported()

  async function toggle() {
    open = !open
    note = ''
    if (open && supported) pushState().then((s) => ((on = s.on), (names = s.names))).catch(() => {})
  }

  async function run(fn, done = '') {
    busy = true
    note = ''
    try {
      await fn()
      note = done
    } catch (e) {
      note = String(e)
    }
    busy = false
  }

  const flip = () =>
    run(async () => {
      if (on) await pushOff()
      else await pushOn()
      on = !on
      if (!on) names = false
    })
  const setNames = () =>
    run(async () => {
      await pushNames(!names)
      names = !names
    })
</script>

<div class="wrap">
  <button class="bell" class:on onclick={toggle} aria-label="Notifications" aria-expanded={open} title="Notifications">
    <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path d="M6 17.5V11a6 6 0 0 1 12 0v6.5l1.5 1.5h-15Z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" /><path d="M10 21.5h4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
  </button>
  {#if open}
    <div class="panel" role="dialog" aria-label="Notifications">
      {#if !supported}
        <p>On iPhone, add Dyna to the Home Screen first. Then notifications work.</p>
      {:else}
        <label class="row">
          <span>Notifications</span>
          <input type="checkbox" role="switch" checked={on} disabled={busy} onchange={flip} />
        </label>
        <label class="row">
          <span>Show room and sender names</span>
          <input type="checkbox" role="switch" checked={names} disabled={busy || !on} onchange={setNames} />
        </label>
        <button class="test" disabled={busy || !on} onclick={() => run(pushTest, 'Test sent.')}>Send a test</button>
        {#if note}<p class="note">{note}</p>{/if}
      {/if}
    </div>
  {/if}
</div>

<style>
  .wrap { position: relative; flex: none; }
  .bell { width: 32px; height: 32px; border-radius: 6px; display: grid; place-items: center; color: var(--muted); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .bell:hover { background: var(--hover); color: var(--ink); }
  }
  .bell:active { background: var(--select); }
  .bell.on { color: var(--iris); }
  .panel { position: absolute; bottom: 40px; right: -44px; width: 216px; z-index: 20; background: var(--field); border-radius: 8px; padding: var(--s3); display: flex; flex-direction: column; gap: 10px; box-shadow: 0 6px 24px rgba(0, 0, 0, 0.45); }
  .row { display: flex; align-items: center; justify-content: space-between; gap: 10px; font-size: var(--t-ui); color: var(--ink-2); }
  .test { height: 32px; border-radius: 6px; background: var(--select); color: var(--ink); font-size: var(--t-ui); font-weight: 600; }
  .test:disabled { opacity: 0.5; cursor: default; }
  p { margin: 0; font-size: var(--t-sm); line-height: var(--leading); color: var(--muted); }
  .note { color: var(--faint); }
</style>

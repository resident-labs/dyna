<script lang="ts">
  // The full-window settings modal, like Discord's user settings: a nav on
  // the left, one pane on the right, Esc to close. Settings and Room
  // settings both use it. nav is [{id, label, dot}]; dot is a title for a
  // gold dot after the label. busy: a modal or an inline editor inside the
  // pane owns Esc.
  import type { Snippet } from 'svelte'
  import { fly } from 'svelte/transition'
  import { dialog as modal } from './dialog.ts'
  import { BASE, motion } from './motion.ts'

  interface NavItem {
    id: string
    label: string
    dot?: string
  }

  interface Props {
    label: string
    title: string
    nav: NavItem[]
    pane: string
    onselect: (id: string) => void
    busy?: boolean
    wide?: boolean
    onclose: () => void
    foot?: Snippet | null
    children: Snippet
  }

  let { label, title, nav, pane, onselect, busy = false, wide = false, onclose, foot = null, children }: Props = $props()
</script>

<div class="shell" role="dialog" aria-modal="true" aria-label={label} tabindex="-1" use:modal={{ close: onclose, busy }}>
  <nav>
    <div class="navin">
      <h2>{title}</h2>
      {#each nav as n}
        <button class:sel={pane === n.id} onclick={() => onselect(n.id)}>
          <span class="label">{n.label}</span>
          {#if n.dot}<span class="dot" title={n.dot}></span>{/if}
        </button>
      {/each}
      {#if foot}<p class="foot">{@render foot()}</p>{/if}
    </div>
  </nav>
  <main class:wide>
    <div class="pane" class:wide>{#key pane}<div in:fly={{ y: 6, duration: motion(BASE) }}>{@render children()}</div>{/key}</div>
    <button class="close" onclick={onclose} aria-label="Close {label.toLowerCase()}">
      <span class="x"><svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg></span>
      <span class="esc">ESC</span>
    </button>
  </main>
</div>

<style>
  /* Above the phone drawer (50); below the profile editor (55), menus and Confirm. */
  .shell { position: fixed; inset: 0; z-index: 52; display: flex; background: var(--main); outline: none; }
  nav { flex: 1 0 220px; background: var(--side); display: flex; justify-content: flex-end; }
  .navin { width: 232px; padding: 56px var(--s2) 20px 20px; display: flex; flex-direction: column; gap: 2px; }
  nav h2 { font-size: var(--t-sm); font-weight: 700; color: var(--faint); text-transform: uppercase; letter-spacing: 0.06em; margin: 0 10px var(--s2); }
  nav button { display: flex; align-items: center; justify-content: space-between; padding: 7px 10px; border-radius: 5px; color: var(--muted); font-size: var(--t-lead); font-weight: 500; text-align: left; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    nav button:hover { background: var(--hover); color: var(--ink); }
  }
  nav button:active { background: var(--select); }
  nav button.sel { background: var(--select); color: var(--ink); }
  .label { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--gold); }
  .foot { margin: 18px 10px 0; font-size: var(--t-sm); color: var(--faint); line-height: var(--leading); }
  main { flex: 1 1 800px; display: flex; min-width: 0; }
  .pane { width: min(760px, 100%); padding: 56px 40px 60px; overflow-y: auto; }
  main.wide { flex-basis: 1160px; }
  .pane.wide { width: min(1160px, 100%); }
  .close { align-self: flex-start; margin: 56px 0 0 var(--s2); display: flex; flex-direction: column; align-items: center; gap: 6px; color: var(--muted); }
  .x { width: 36px; height: 36px; border-radius: 50%; border: 2px solid var(--line); display: grid; place-items: center; }
  .close .x { transition: border-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .close:hover .x { border-color: var(--ink-2); color: var(--ink); }
  }
  .esc { font-size: var(--t-sm); font-weight: 600; }

  /* A phone: the section list becomes a row of tabs over the pane, and the
     close button sits at the top right. */
  @media (max-width: 760px) {
    .shell { flex-direction: column; padding-top: env(safe-area-inset-top); padding-bottom: env(safe-area-inset-bottom); }
    /* A title row with the close button, then the sections as equal tabs
       across the full width, so none hides under the close button. */
    nav { flex: none; position: relative; justify-content: flex-start; border-bottom: 1px solid var(--line-soft); padding-top: 52px; }
    .navin { width: 100%; flex-direction: row; flex-wrap: wrap; padding: 0 var(--s2) var(--s2); gap: var(--s1); }
    nav h2 { position: absolute; top: 18px; left: 16px; margin: 0; }
    .foot { display: none; }
    /* A long label wraps inside its tab instead of ending in an ellipsis, so
       "Review & landing" is never cut to "Review & lan…". */
    nav button { flex: 1 1 calc(25% - 4px); min-width: 0; justify-content: center; padding: 10px var(--s1); min-height: 44px; white-space: normal; line-height: 1.25; text-align: center; font-size: var(--t-body); }
    .label { overflow: visible; }
    main { flex: 1; min-height: 0; }
    .pane { width: 100%; padding: 20px var(--s4) 40px; }
    .close { position: absolute; top: calc(env(safe-area-inset-top) + 6px); right: 6px; margin: 0; width: 44px; height: 44px; justify-content: center; }
    .esc { display: none; }
  }
  @media (max-width: 420px) { nav button { font-size: var(--t-sm); padding-inline: 2px; } }
</style>

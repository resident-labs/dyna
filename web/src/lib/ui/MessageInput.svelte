<script>
  import { tick } from 'svelte'
  import ForwardQuote from './ForwardQuote.svelte'
  import Tray from '../files/Tray.svelte'
  // skin: 'docked' is the look in Messages and rooms. 'glass' is for a floating
  // surface over a picture: a dark field with an iris edge.
  let { room, forwarding = null, onremoveforward, onopenfile, skin = 'docked', attachments, draft = $bindable(''), field = $bindable(), placeholder, busy = false, closed = false, canSend, joined = false, bad = false, spawn = false, oninput = () => {}, onkeydown, onclick = undefined, onsend } = $props()
  let picker = $state()
  function grow() {
    if (!field) return
    field.style.height = 'auto'
    field.style.height = Math.min(field.scrollHeight, 240) + 'px'
  }
  $effect(() => { draft; tick().then(grow) })
</script>

  <div class="box" class:glass={skin === 'glass'} class:joined class:bad class:spawn>
    {#if forwarding}<ForwardQuote {room} post={forwarding} onremove={onremoveforward} disabled={busy || closed} {onopenfile} />{/if}
    <Tray items={$attachments.items} onremove={attachments.remove} onretry={attachments.retry} />
    <div class="row">
    <button class="plus" onclick={() => picker.click()} disabled={closed} aria-label="Attach files" title="Attach files">
      <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><circle cx="10" cy="10" r="8" fill="currentColor" /><path d="M10 6v8M6 10h8" stroke="var(--field)" stroke-width="2" stroke-linecap="round" /></svg>
    </button>
    <input bind:this={picker} type="file" multiple hidden onchange={(e) => { attachments.addFiles(e.currentTarget.files); e.currentTarget.value = '' }} />
    <textarea
      bind:this={field}
      bind:value={draft}
      rows="1"
      spellcheck="true"
      {placeholder}
      oninput={() => { grow(); oninput() }}
      {onkeydown}
      {onclick}
      onpaste={attachments.paste}
      onfocus={grow}
      readonly={busy || closed}
    ></textarea>
    <button class="send" onclick={onsend} disabled={!canSend} aria-label="Send">
      <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d="M3 10h11M10 5l5 5-5 5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
    </button>
    </div>
  </div>

<style>
  .box {
    display: flex;
    flex-direction: column;
    background: var(--field);
    border-radius: 8px;
    border: 1px solid transparent;
  }
  .row { display: flex; align-items: flex-end; }
  .plus { flex: none; height: 44px; padding: 0 var(--s1) 0 14px; color: var(--muted); }
  .plus:not(:disabled) { transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .plus:hover:not(:disabled) { color: var(--ink); }
  }
  .box.joined { border-top-left-radius: 0; border-top-right-radius: 0; }
  .box:focus-within { border-color: #3d3b40; }
  .box.glass { --field: rgba(14, 14, 16, 0.55); border-radius: var(--r-sm); border-color: var(--iris-edge); }
  .box.glass:focus-within { border-color: rgba(183, 168, 255, 0.7); }
  .box.glass textarea { font-size: var(--t-ui); }
  .box.glass.bad, .box.glass.bad:focus-within { border-color: color-mix(in srgb, var(--coral) 45%, transparent); }
  .box.bad { border-color: color-mix(in srgb, var(--coral) 45%, transparent); }
  .box.spawn { border-color: color-mix(in srgb, var(--sage) 45%, transparent); }
  textarea {
    flex: 1;
    resize: none;
    background: transparent;
    border: 0;
    outline: none;
    color: var(--ink);
    font: inherit;
    font-size: var(--t-lead);
    line-height: 22px;
    padding: 11px 0 11px 10px;
    max-height: 240px;
    min-height: 44px;
  }
  textarea::placeholder { color: #6f6a62; }
  .send { padding: 0 14px; height: 44px; color: var(--muted); }
  .send:disabled { color: #4d4a45; cursor: default; }
  .send:not(:disabled) { transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .send:not(:disabled):hover { color: var(--ink); }
  }
  @media (max-width: 760px) {
    textarea { font-size: var(--t-title); max-height: 160px; }
    .plus { padding: 0 var(--s2) 0 var(--s3); }
    .send { padding: 0 var(--s4); }
  }
</style>

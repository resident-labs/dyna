<script lang="ts">
  import type { Snippet } from 'svelte'
  import { tick } from 'svelte'
  import ForwardQuote from './ForwardQuote.svelte'
  import Tray from '../files/Tray.svelte'
  import type { AttachmentDraft } from '../files/attachmentDraft.ts'
  import type { Attachment, Room } from '../api/room.ts'
  import type { ThreadPost } from '../sessions/dm.ts'

  interface Props {
    // room names the feed a forwarded post comes from.
    room?: Pick<Room, 'id' | 'name'>
    forwarding?: ThreadPost | null
    onremoveforward?: (() => void) | undefined
    onopenfile?: (attachment: Attachment) => void
    // skin draws a floating box over a computer view.
    skin?: 'docked' | 'glass'
    // attachments is the draft's file list, when this box takes files.
    attachments?: AttachmentDraft
    draft?: string
    // field is the box's element, so a view can focus it and the box can grow it.
    field?: HTMLElement
    placeholder?: string
    label?: string
    maxlength?: number
    busy?: boolean
    closed?: boolean
    canSend: boolean
    joined?: boolean
    // bad marks a failed send; spawn marks a queued session start.
    bad?: boolean
    spawn?: boolean
    oninput?: () => void
    onreference?: (() => void) | undefined
    onkeydown?: ((event: KeyboardEvent) => unknown) | undefined
    onclick?: ((event: MouseEvent) => void) | undefined
    onsend: () => void
    tools?: Snippet | null
    // context draws between the file tray and the textarea.
    context?: Snippet | null
    model?: string
    mode?: string
    submitLabel?: string
    expand?: boolean
    // completionID and activeOption describe an open completion list.
    completionID?: string
    activeOption?: string
    expanded?: boolean
  }
  let { room = undefined, forwarding = null, onremoveforward = undefined, onopenfile = undefined, skin = 'docked', attachments = undefined,
    draft = $bindable(''), field = $bindable(), placeholder, label = placeholder, maxlength = undefined,
    busy = false, closed = false, canSend, joined = false, bad = false, spawn = false,
    oninput = () => {}, onreference = undefined, onkeydown = undefined, onclick = undefined, onsend, tools = undefined, context = undefined,
    model = '', mode = 'Message', submitLabel = '', expand = false,
    completionID = undefined, activeOption = undefined, expanded = false }: Props = $props()
  let picker = $state<HTMLInputElement | undefined>()
  let focused = $state(false)
  function grow() {
    if (!field) return
    field.style.height = 'auto'
    const lines = expand && focused ? 3 : 1
    field.style.height = Math.min(Math.max(lines * 22 + 24, field.scrollHeight), 240) + 'px'
  }
  $effect(() => { draft; tick().then(grow) })
  function keydown(event: KeyboardEvent) {
    if (event.isComposing || event.keyCode === 229) return
    if (onkeydown) onkeydown(event)
    else if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); if (canSend && !busy && !closed) onsend() }
  }
</script>

<div class="box" class:glass={skin === 'glass'} class:joined class:bad class:spawn
  onfocusout={(event) => { const next = event.relatedTarget; if (!(next instanceof Node && event.currentTarget.contains(next))) { focused = false; grow() } }}>
  {#if forwarding && room}<ForwardQuote {room} post={forwarding} onremove={onremoveforward} disabled={busy || closed} {onopenfile} />{/if}
  {#if attachments}
    {@const tray = $attachments?.items ?? []}
    <Tray items={tray} onremove={attachments.remove} onretry={attachments.retry} />
  {/if}
  {@render context?.()}
  <textarea bind:this={field} bind:value={draft} rows="1" spellcheck="true" {placeholder} {maxlength}
    aria-label={label} role={completionID ? 'combobox' : undefined} aria-autocomplete={completionID ? 'list' : undefined}
    aria-controls={completionID} aria-expanded={completionID ? expanded : undefined} aria-activedescendant={activeOption}
    oninput={() => { grow(); oninput() }} onkeydown={keydown} {onclick}
    onpaste={attachments?.paste} onfocus={() => { focused = true; grow() }} readonly={busy || closed}></textarea>
  <div class="toolbar">
    <div class="tools">
      {@render tools?.()}
      {#if model}<span class="chip model" title={model}>{model}</span>{/if}
      {#if mode && mode !== 'Message'}<span class="chip mode">{mode}</span>{/if}
      {#if attachments}
        <button class="tool" type="button" onclick={() => picker?.click()} disabled={busy || closed} aria-label="Attach files" title="Attach files">
          <svg viewBox="0 0 20 20" width="17" height="17" aria-hidden="true"><path d="m7 11 5-5a2.1 2.1 0 0 1 3 3l-6 6a3.5 3.5 0 0 1-5-5l6-6" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
        </button>
        <input bind:this={picker} type="file" multiple hidden onchange={(event) => { attachments.addFiles(event.currentTarget.files); event.currentTarget.value = '' }} />
      {/if}
      {#if onreference}<button class="tool reference" type="button" onclick={onreference} disabled={busy || closed} aria-label="Attach a PR or issue" title="Attach a PR or issue">#</button>{/if}
    </div>
    <button class="send" class:labeled={!!submitLabel} type="button" onclick={onsend} disabled={!canSend || busy || closed} aria-label={submitLabel || 'Send'}>
      {#if submitLabel}{submitLabel}{:else}<svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d="M10 15V5m-5 5 5-5 5 5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>{/if}
    </button>
  </div>
</div>

<style>
  .box { display: flex; flex-direction: column; min-width: 0; background: var(--field); border: 1px solid var(--line); border-radius: 12px; transition: border-color var(--motion-fast) var(--ease); }
  .box:focus-within { border-color: var(--iris-edge); }
  .box.joined { border-top-left-radius: 0; border-top-right-radius: 0; }
  .box.bad { border-color: color-mix(in srgb, var(--coral) 55%, transparent); }
  .box.spawn { border-color: color-mix(in srgb, var(--sage) 50%, transparent); }
  .box.glass { --field: rgba(14, 14, 16, 0.75); border-color: var(--iris-edge); }
  textarea { width: 100%; min-width: 0; min-height: 68px; max-height: 240px; resize: none; background: transparent; border: 0; outline: none; color: var(--ink); font: inherit; font-size: var(--t-lead); line-height: 22px; padding: 12px 14px; }
  textarea::placeholder { color: var(--faint); }
  .toolbar { display: flex; align-items: center; justify-content: space-between; gap: var(--s2); padding: 0 8px 8px; }
  .tools { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; min-width: 0; }
  .chip { padding: 4px 8px; border: 1px solid var(--line); border-radius: 6px; color: var(--muted); font-size: var(--t-sm); max-width: 180px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mode { border-color: transparent; color: var(--faint); }
  .tool { flex: none; display: grid; place-items: center; width: 30px; height: 30px; border-radius: 6px; color: var(--muted); }
  .tool:not(:disabled) { transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  .reference { font-family: var(--mono); font-size: var(--t-title); }
  .send { display: grid; place-items: center; flex: none; width: 32px; height: 32px; border-radius: 8px; background: var(--iris); color: var(--rail); }
  .send.labeled { width: auto; padding: 0 12px; font-size: var(--t-sm); font-weight: 600; }
  .send:disabled { background: var(--raise); color: var(--faint); cursor: default; }
  button:disabled { opacity: .55; }
  @media (hover: hover) { .tool:hover:not(:disabled) { color: var(--ink); background: var(--hover); } .send:hover:not(:disabled) { filter: brightness(1.1); } }
  .tool:active:not(:disabled) { background: var(--select); }
  @media (max-width: 760px) { textarea { font-size: var(--t-title); max-height: 160px; } .tool, .send { min-width: 44px; height: 44px; } .toolbar { padding: 0 6px 6px; } .chip { max-width: 130px; } }
</style>

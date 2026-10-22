<script>
  import Avatar from '../ui/Avatar.svelte'
  import CardFrame from './CardFrame.svelte'
  import { fileURL } from '../../api.js'
  import { ago } from '../ui/format.js'
  import { visuals } from './visuals.svelte.js'
  import { visualOf, versionOf, checkStatus, checksSummary } from './visuals.js'

  // The full view of one card: header with versions and checks, then the card.
  let { roomId, att, ready, onjump = null, ondownload, downloading = false, error = '', onclose } = $props()
  // svelte-ignore state_referenced_locally
  let shownVersion = $state(att.version || 1)
  let open = $state('') // 'versions' or 'checks'
  let shot = $state(null)

  let row = $derived(visualOf(visuals.byId, att.postId, att))
  let versions = $derived(row?.versions?.length ? row.versions : [{ post: row?.post, attachment: att }])
  let current = $derived(versionOf(versions, shownVersion))
  let shown = $derived(current.attachment)
  let older = $derived(current !== versions[0])
  let title = $derived(shown.title || shown.name)
  let status = $derived(checkStatus(shown))
  const when = (post) => (post ? ago(new Date(post.ts).toISOString()) : '')
  const note = (post) => post?.text?.split('\n').find((l) => l.trim()) ?? ''
  const toggle = (name) => { open = open === name ? '' : name; shot = null }
  function pick(n) { shownVersion = n; open = '' }
</script>

<header>
  <span class="name">{title}</span>
  <div class="anchor">
    <button class="pill-btn" aria-haspopup="menu" aria-expanded={open === 'versions'} onclick={() => toggle('versions')}>v{shown.version || 1} <span aria-hidden="true">▾</span></button>
    {#if open === 'versions'}
      <ul class="menu" role="menu" aria-label="Versions">
        {#each versions as v (v.attachment.version || 1)}
          <li role="none"><button role="menuitem" class:on={v === current} onclick={() => pick(v.attachment.version || 1)}>
            <span class="pill mono">v{v.attachment.version || 1}</span>
            <span class="what"><span>{v.post?.speaker} · {when(v.post)}</span><span class="note">{note(v.post)}</span></span>
          </button></li>
        {/each}
      </ul>
    {/if}
  </div>
  <span class="meta">{current.post?.speaker ?? 'visuals'} · {when(current.post)}</span>
  <span class="grow"></span>
  <div class="anchor">
    <button class="checks" aria-haspopup="dialog" aria-expanded={open === 'checks'} onclick={() => toggle('checks')}>
      <span class="dot {status}"></span>{checksSummary(shown)}
    </button>
    {#if open === 'checks'}
      <div class="pop" role="dialog" aria-label="Checks">
        {#if !shown.checks?.length}
          <p class="none">No checks for this version</p>
        {:else}
          <ul class="list">
            {#each shown.checks as c}
              <li><span class="dot {c.status}"></span><span class="cname">{c.name}</span>{#if c.status !== 'pass' && c.detail}<span class="detail">{c.detail}</span>{/if}</li>
            {/each}
          </ul>
        {/if}
        {#if shown.shots?.length}
          <div class="shots">
            {#each shown.shots as s}
              <button class:on={shot === s.id} aria-label="Show the {s.width} pixel screenshot" onclick={() => (shot = shot === s.id ? null : s.id)}>
                <img src={fileURL(roomId, s.id)} alt="" loading="lazy" /><span class="mono">{s.width}</span>
              </button>
            {/each}
          </div>
          {#if shot}<img class="big" src={fileURL(roomId, shot)} alt="Screenshot at {shown.shots.find((s) => s.id === shot)?.width} pixels wide" />{/if}
        {/if}
      </div>
    {/if}
  </div>
  {#if onjump}<button class="btn" onclick={onjump}>Jump to post</button>{/if}
  <button class="btn" onclick={() => ondownload(shown)} disabled={downloading} aria-label={`Download ${shown.name}`}>{downloading ? 'Saving…' : 'Download'}</button>
  <button class="btn" onclick={onclose} aria-label="Close preview">Close</button>
</header>
{#if error}<p class="err" role="alert">{error}</p>{/if}
{#if older}
  <div class="strip">Viewing v{shown.version || 1} · <button onclick={() => pick(versions[0].attachment.version || 1)}>Back to latest</button></div>
{/if}
<!-- A new frame per version, even for the same bytes: switching the URL of one frame reads as a navigation to its link guard, and each version has its own network policy. -->
<div class="body">{#if ready}{#key current.post?.id ?? shown.id}<CardFrame {roomId} att={shown} />{/key}{/if}</div>

<style>
  header { display: flex; align-items: center; gap: 10px; padding: 10px 16px; border-bottom: 1px solid var(--line-soft); flex: none; position: relative; z-index: 1; }
  .name { font-weight: 600; font-size: 14px; color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .meta { color: var(--muted); font-size: 12px; white-space: nowrap; }
  .grow { flex: 1; }
  .anchor { position: relative; flex: none; }
  .pill-btn, .checks, .btn { color: var(--ink-2); border: 1px solid var(--line); border-radius: 6px; padding: 5px 10px; font-size: 12.5px; display: flex; align-items: center; gap: 7px; flex: none; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .pill-btn:hover, .checks:hover, .btn:hover { background: var(--hover); color: var(--ink); }
  }
  .pill-btn:active, .checks:active, .btn:active { background: var(--select); }
  .btn:disabled { opacity: 0.5; pointer-events: none; }
  .menu, .pop { position: absolute; top: calc(100% + 6px); background: var(--raise); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 12px 40px #0008; padding: 6px; z-index: 2; margin: 0; animation: pop var(--motion-fast) var(--ease); }
  .menu { left: 0; list-style: none; min-width: 300px; }
  .pop { right: 0; width: 380px; max-width: 80vw; padding: 12px; }
  @keyframes pop { from { opacity: 0; transform: translateY(-4px); } }
  .menu button { display: flex; gap: 10px; align-items: center; width: 100%; text-align: left; padding: 8px; border-radius: 6px; font-size: 13px; color: var(--ink); transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) { .menu button:hover { background: var(--hover); } }
  .menu button.on, .menu button:active { background: var(--select); }
  .what { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .note { color: var(--muted); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .pill { background: var(--select); color: var(--iris); border-radius: 10px; padding: 1px 7px; font-size: 11px; flex: none; transition: background-color var(--motion-fast) var(--ease); }
  .menu .on .pill, .menu button:hover .pill { background: var(--raise); }
  .dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--faint); }
  .dot.pass { background: var(--sage); }
  .dot.warn { background: var(--gold); }
  .dot.fail { background: var(--coral); }
  .list { list-style: none; margin: 0 0 10px; padding: 0; display: grid; gap: 8px; }
  .list li { display: grid; grid-template-columns: 8px 1fr; column-gap: 8px; row-gap: 2px; align-items: center; font-size: 13px; }
  .cname { color: var(--ink); }
  .detail { grid-column: 2; color: var(--muted); font-size: 12px; overflow-wrap: anywhere; }
  .none { margin: 0; color: var(--muted); font-size: 13px; }
  .shots { display: flex; gap: 8px; }
  .shots button { flex: 1; display: flex; flex-direction: column; gap: 4px; align-items: center; padding: 4px; border: 1px solid var(--line-soft); border-radius: 6px; color: var(--muted); font-size: 11px; min-width: 0; }
  .shots button.on, .shots button:hover { border-color: var(--iris-edge); }
  .shots img { width: 100%; height: 64px; object-fit: cover; object-position: top; border-radius: 3px; }
  .big { display: block; width: 100%; max-height: 360px; object-fit: contain; margin-top: 10px; border-radius: 4px; background: var(--main); }
  .strip { padding: 7px 16px; font-size: 12.5px; color: var(--gold); background: var(--gold-wash); border-bottom: 1px solid var(--line-soft); flex: none; }
  .strip button { color: var(--gold); text-decoration: underline; }
  .err { color: var(--coral); font-size: 13px; margin: 0; padding: 8px 16px; flex: none; }
  .body { display: flex; flex-direction: column; flex: 1; min-height: 0; background: #18171a; }
  @media (max-width: 600px) { header { flex-wrap: wrap; padding: 10px 12px; } .meta { display: none; } .grow { display: none; } .anchor { position: static; } .menu, .pop { left: 0; right: 0; width: auto; min-width: 0; max-width: none; max-height: calc(100dvh - 150px); overflow: auto; } }
</style>

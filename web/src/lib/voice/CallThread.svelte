<script>
  // The call's conversation rows (design option A): who spoke, to whom, and
  // how it reached the call, Voice's requests to root and its room checks as
  // quiet activity lines, then the owner's words as they are heard. A shared
  // file is its own quiet line, never a card in a message; onopen opens it.
  // The floating widget and the phone's sheet both show it.
  import Avatar from '../ui/Avatar.svelte'
  import VoiceRelay from './VoiceRelay.svelte'
  import { icons } from './icons.js'
  import { NAMES, shareTitle, shareWhat } from './conversation.js'

  let { rows, partial = '', onopen } = $props()
  const HUES = { you: 4, voice: 3, root: 7 }
  const time = (ts) => new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
</script>

{#each rows as r (r.key)}
  {#if r.share}
    <VoiceRelay icon="shared" label="{NAMES[r.share.who]} shared {shareWhat(r.share.att)}" text={shareTitle(r.share.att)} onopen={() => onopen(r.share)} />
  {:else if r.tool}
    <VoiceRelay icon="search" label={r.tool.label} state={r.tool.state} />
  {:else if r.ask}
    <VoiceRelay icon="mic" label="Started" text={r.post.text} state={r.state} />
  {:else}
    <article class="tp">
      <div class="gutter"><Avatar name={NAMES[r.who]} hue={HUES[r.who]} owner={r.who === 'you'} size={28} /></div>
      <div>
        <header>
          <span class="name hue-{HUES[r.who]}">{NAMES[r.who]}</span>
          <time>{time(r.post.ts)}</time>
          <span class="to">→ {r.to}</span>
          {#if r.via}<span class="to">via Voice</span>{/if}
          {#if r.tag}<span class="tag">{#if r.tag === 'spoken'}<svg viewBox="0 0 24 24" width="11" height="11" aria-hidden="true"><path d={icons.mic} fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" /></svg>{/if}{r.tag}</span>{/if}
        </header>
        <div class="prose">{r.post.text}</div>
      </div>
    </article>
  {/if}
{:else}
  {#if !partial}<p class="empty">Say something. Your words and root's replies appear here.</p>{/if}
{/each}
{#if partial}
  <article class="tp partial" aria-live="polite">
    <div class="gutter"><Avatar name="you" hue={4} owner size={28} /></div>
    <div><header><span class="name hue-4">you</span><span class="tag">hearing</span></header><div class="prose">{partial}</div></div>
  </article>
{/if}
<style>
  .tp { display: grid; grid-template-columns: 44px 1fr; padding: 2px var(--s3) 2px 0; margin-top: 10px; }
  .gutter { display: flex; justify-content: center; padding-top: 2px; }
  .tp header { display: flex; align-items: baseline; flex-wrap: wrap; column-gap: 6px; line-height: var(--leading-tight); }
  .name { font-size: var(--t-ui); font-weight: 600; }
  .hue-3 { color: var(--hue-3); }
  .hue-4 { color: var(--hue-4); }
  .hue-7 { color: var(--hue-7); }
  time, .to { font-size: var(--t-sm); color: var(--faint); }
  .tag {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    font-size: var(--t-cap);
    font-weight: 600;
    color: var(--muted);
    background: var(--raise);
    border: 1px solid var(--line);
    border-radius: 4px;
    padding: 0 5px;
    line-height: 16px;
    align-self: center;
  }
  .prose { margin-top: 1px; font-size: var(--t-ui); color: var(--ink-2); overflow-wrap: anywhere; white-space: pre-wrap; }
  .partial .prose { color: var(--muted); }
  .empty { margin: var(--s2) var(--s3); font-size: var(--t-sm); color: var(--faint); }
</style>

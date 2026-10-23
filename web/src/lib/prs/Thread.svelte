<script lang="ts">
  // The thread of a pull request, an issue or a local change as a timeline: avatars on a
  // vertical line, the description and the comments as cards, and the
  // rounds of the reviewer (timeline in github.js) as the round snippet draws
  // them. The after snippet adds one-line events at the end, then one line per
  // bot. Bots never block, so they are only a note here.
  // opener names who opened it (authorName), when the host knows more than GitHub's login.
  import { ago, markdown } from '../ui/format.ts'
  import type { Snippet } from 'svelte'
  import type { BotReview, ReviewRound } from '../api/change.ts'
  import type { GitHubDetail } from '../api/github.ts'
  import { isBotAuthor, botStateWord } from './prlist.ts'
  import type { ThreadEntry, TimelineEntry } from './github.ts'

  interface Props {
    detail?: Pick<GitHubDetail, 'author' | 'createdAt' | 'body'> | null
    opener?: string
    events?: TimelineEntry[]
    bots?: BotReview[] | null
    round?: Snippet<[ReviewRound]> | null
    after?: Snippet | null
  }
  let { detail = null, opener = '', events = [], bots = [], round = null, after = null }: Props = $props()
  let showAll = $state(new Set<number>()) // long comments the owner expanded
  const longBody = (b?: string | null) => (b || '').length > 1400
  let first = $derived(opener || detail?.author?.login)
  const hue = (login?: string) => [...(login || '')].reduce((n, c) => n + c.charCodeAt(0), 0) % 6
</script>

{#snippet who(login?: string)}
  <span class="av bg-{hue(login)}" aria-hidden="true">{(login || '?').charAt(0).toUpperCase()}</span>
{/snippet}
{#snippet post(t: ThreadEntry, i: number)}
  <div class="it">
    {@render who(t.author?.login)}
    <article class="post" class:bot={isBotAuthor(t.author)}>
      <header>
        <strong>{t.author?.login}</strong>
        {#if t.review}<span class="rstate {t.tone}">{t.review}</span>{/if}
        <span>{ago(t.at)}</span>
      </header>
      {#if t.body?.trim()}
        <div class="prose" class:clamp={longBody(t.body) && !showAll.has(i)}>{@html markdown(t.body)}</div>
        {#if longBody(t.body) && !showAll.has(i)}
          <button class="link more-btn" onclick={() => (showAll = new Set([...showAll, i]))}>Show all</button>
        {/if}
      {/if}
    </article>
  </div>
{/snippet}

<div class="tl">
  {#if detail}
  <div class="it">
    {@render who(first)}
    <article class="post first">
      <header><strong>{first}</strong><span>opened this · {ago(detail.createdAt)}</span></header>
      {#if detail.body?.trim()}
        <div class="prose">{@html markdown(detail.body)}</div>
      {:else}
        <p class="none">No description.</p>
      {/if}
    </article>
  </div>
  {/if}
  {#each events as e, i (i)}
    {#if e.kind === 'round' && round}{@render round(e.round)}{:else if e.kind === 'post'}{@render post(e.post, i)}{/if}
  {/each}
  {#if detail && !events.length}<p class="quiet">No comments yet.</p>{/if}
  {@render after?.()}
  {#each bots ?? [] as b}
    <p class="it ev"><span class="av small" aria-hidden="true">💬</span><strong>{b.bot}</strong> {botStateWord(b.state)}{b.findings ? ` · ${b.findings} ${b.findings === 1 ? 'finding' : 'findings'}` : ''} · <span class="ok">does not block</span></p>
  {/each}
</div>

<style>
  .tl { position: relative; margin-top: 12px; padding-left: 40px; }
  .tl::before { content: ''; position: absolute; left: 15px; top: 0; bottom: 0; width: 2px; background: var(--line-soft); }
  .tl :global(.it) { position: relative; margin: 0 0 14px; }
  .tl :global(.av) { position: absolute; left: -40px; top: 0; width: 32px; height: 32px; display: grid; place-items: center; border-radius: 50%; font-size: 13px; font-weight: 700; color: var(--h, var(--muted)); background: color-mix(in srgb, var(--h, var(--raise)) 20%, var(--main)); }
  .tl :global(.av.small) { width: 22px; height: 22px; left: -35px; font-size: 12px; background: var(--raise); }
  .tl :global(.ev) { font-size: 12.5px; color: var(--muted); padding: 4px 0; min-height: 22px; }
  .tl :global(.ev strong) { color: var(--ink-2); }
  .tl :global(.ev .ok) { color: var(--sage); }
  .post { background: var(--side); border: 1px solid var(--line-soft); border-radius: 10px; overflow: hidden; }
  .post > header { display: flex; align-items: baseline; gap: 8px; padding: 7px 12px; border-bottom: 1px solid var(--line-soft); font-size: 12.5px; color: var(--faint); }
  .post > header strong { color: var(--ink); font-size: 13px; }
  .post.bot > header strong { color: var(--muted); }
  .rstate { font-weight: 600; font-size: 11.5px; }
  .rstate.sage { color: var(--sage); }
  .rstate.gold { color: var(--gold); }
  .rstate.quiet { color: var(--muted); }
  .prose { padding: 4px 14px 8px; font-size: 14px; overflow-wrap: anywhere; }
  .prose.clamp { max-height: 260px; overflow: hidden; mask-image: linear-gradient(#000 70%, transparent); }
  .link { font-size: 12.5px; color: var(--iris); font-weight: 600; padding: 0; }
  .link:hover { text-decoration: underline; }
  .more-btn { margin: 0 14px 10px; }
  .none { margin: 0; padding: 10px 14px; color: var(--faint); font-size: 13px; font-style: italic; }
  .quiet { color: var(--faint); font-size: 13.5px; padding: 12px 2px; }
</style>

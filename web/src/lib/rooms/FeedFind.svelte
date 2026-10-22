<script lang="ts">
  // Find in this room. The feed renders only the rows on screen and holds only
  // a window of the log, so the server searches the whole log
  // (roomsvc.SearchFeed). A result jumps the feed to its post.
  import { onMount, tick } from 'svelte'
  import { api } from '../../api.js'
  import { isFeedSearch } from '../api/guards.ts'
  import type { Post } from '../api/room.ts'
  import { snippet } from './feedFind.ts'
  import { speaker } from '../ui/posts.js'
  import { stamp } from '../ui/format.js'
  import SearchIcon from '../ui/SearchIcon.svelte'

  interface Props {
    roomId: string
    roomName: string
    initial?: string // the text the bar opens with
    onjump: (id: string) => void
    onclose: () => void
  }
  let { roomId, roomName, initial = '', onjump, onclose }: Props = $props()

  let text = $state('')
  let input = $state<HTMLInputElement>()
  let results = $state<{ query: string; posts: Post[]; more: boolean } | null>(null)
  let error = $state('')
  let loading = $state(false)
  let list = $state<HTMLElement>()
  let pick = $state(-1) // the highlighted result
  let at = -1 // the result the feed last jumped to
  let asked = '' // the last query sent
  let token = 0
  let timer: ReturnType<typeof setTimeout> | undefined

  // focus selects the box; a query replaces its text.
  export async function focus(query = '') {
    if (query) text = query
    await tick()
    input?.focus()
    input?.select()
  }
  onMount(() => focus(initial))

  // search asks the server; an answer to an older query is dropped.
  async function search(query: string) {
    clearTimeout(timer)
    asked = query
    const mine = ++token
    loading = true
    try {
      const found: unknown = await api.searchFeed(roomId, query)
      if (mine !== token) return
      if (!isFeedSearch(found)) throw new Error('The server sent search results this page cannot read.')
      results = { query, posts: found.posts, more: found.more }
      error = ''
      pick = found.posts.length ? 0 : -1
      at = -1
    } catch (e) {
      if (mine !== token) return
      error = e instanceof Error ? e.message : String(e)
      // The same text may be sent again.
      asked = ''
    } finally {
      if (mine === token) loading = false
    }
  }

  // Typing searches after a short pause.
  $effect(() => {
    const query = text.trim()
    if (!query) {
      token++
      loading = false
      asked = ''
      results = null
      error = ''
      return
    }
    if (query === asked) return
    timer = setTimeout(() => search(query), 250)
    return () => clearTimeout(timer)
  })

  function jump(i: number) {
    const p = results?.posts[i]
    if (!p) return
    pick = at = i
    onjump(p.id)
  }

  // Keys move the highlight, so its row scrolls into the list's view.
  async function showPick() {
    await tick()
    list?.querySelector(`#find-row-${pick}`)?.scrollIntoView({ block: 'nearest' })
  }

  function keydown(e: KeyboardEvent) {
    const query = text.trim()
    const n = results?.posts.length ?? 0
    if (e.key === 'Escape') {
      e.preventDefault()
      onclose()
    } else if (e.key === 'Enter' && loading) {
      // Enter never jumps within the results of an older query.
      e.preventDefault()
    } else if (e.key === 'Enter' && query && query !== asked) {
      // Enter does not wait for the typing pause.
      e.preventDefault()
      search(query)
    } else if (e.key === 'Enter' && n) {
      e.preventDefault()
      // Enter opens the highlighted match, then each older one; Shift+Enter goes newer.
      jump(pick !== at ? pick : (pick + (e.shiftKey ? n - 1 : 1)) % n)
      showPick()
    } else if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && n) {
      e.preventDefault()
      pick = Math.min(n - 1, Math.max(0, pick + (e.key === 'ArrowDown' ? 1 : -1)))
      showPick()
    }
  }

  const count = $derived(results ? `${results.posts.length}${results.more ? '+' : ''} found` : '')
</script>

<div class="find" role="search">
  <div class="bar">
    <span class="lens"><SearchIcon size={16} /></span>
    <input bind:this={input} bind:value={text} onkeydown={keydown} placeholder="Find in this room" aria-label="Find in this room" role="combobox" aria-expanded={!!results?.posts.length} aria-controls="find-results" aria-activedescendant={results && pick >= 0 ? `find-row-${pick}` : undefined} aria-busy={loading} autocomplete="off" spellcheck="false" enterkeyhint="search" />
    {#if count}<span class="count" aria-live="polite">{count}</span>{/if}
    <button class="x" onclick={onclose} title="Close (Esc)" aria-label="Close find">✕</button>
  </div>
  {#if error}
    <p class="note err" role="alert">{error}</p>
  {:else if results && !results.posts.length}
    <p class="note">Nothing in this room matches “{results.query}”.</p>
  {:else if results}
    <ul class="results" id="find-results" role="listbox" aria-label="Matching posts" bind:this={list}>
      {#each results.posts as p, i (p.id)}
        <li role="presentation">
          <button id="find-row-{i}" class="row" class:on={pick === i} role="option" aria-selected={pick === i} tabindex="-1" onclick={() => jump(i)}>
            <span class="head"><span class="name hue-{p.fromHue}">{speaker(p, roomName)}</span><time>{stamp(p.ts)}</time></span>
            <span class="text">{#each snippet(p.text, results.query) as part, j (j)}{#if part.hit}<mark>{part.text}</mark>{:else}{part.text}{/if}{/each}</span>
          </button>
        </li>
      {/each}
      {#if results.more}<li class="note" role="presentation">Showing the newest {results.posts.length} matches. Add words to narrow the search.</li>{/if}
    </ul>
  {/if}
</div>

<style>
  .find { flex: none; border-bottom: 1px solid var(--line-soft); background: var(--side); }
  .bar { display: flex; align-items: center; gap: var(--s1); padding: 4px var(--s2) 4px var(--s3); }
  .lens { color: var(--faint); flex: none; display: grid; }
  input { flex: 1; min-width: 0; height: 36px; background: none; border: 0; outline: none; color: var(--ink); font: inherit; font-size: var(--t-body); }
  input::placeholder { color: var(--faint); }
  .count { color: var(--faint); font-size: var(--t-sm); white-space: nowrap; }
  .x { color: var(--muted); width: 32px; height: 32px; border-radius: 6px; font-size: var(--t-ui); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .x:hover { color: var(--ink); background: var(--hover); }
    .row:hover { background: var(--hover); }
  }
  .x:active { color: var(--ink); background: var(--select); }
  .row:active { background: var(--select); }
  .results { list-style: none; margin: 0; padding: 0 var(--s1) var(--s1); max-height: min(40vh, 360px); overflow-y: auto; }
  .row { display: flex; flex-direction: column; gap: 2px; width: 100%; text-align: left; padding: 6px var(--s2); border-radius: 6px; color: var(--ink); transition: background-color var(--motion-fast) var(--ease); }
  .row.on { background: var(--select); }
  .head { display: flex; gap: var(--s2); align-items: baseline; font-size: var(--t-sm); }
  .name { font-weight: 600; }
  time { color: var(--faint); }
  .text { font-size: var(--t-ui); color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  mark { background: color-mix(in srgb, var(--gold) 30%, transparent); color: var(--ink); border-radius: 2px; }
  .note { margin: 0; padding: 6px var(--s3) var(--s2); font-size: var(--t-ui); color: var(--faint); }
  .note.err { color: var(--coral); }
  @media (max-width: 760px) {
    input, .x { min-height: 44px; }
    .row { min-height: 44px; }
  }
</style>

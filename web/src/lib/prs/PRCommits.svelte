<script lang="ts">
  // The commits of a pull request, listed by day like GitHub: the author,
  // the message headline and the short SHA, which opens the commit on GitHub.
  //
  // Props:
  //   commits  [{sha, headline, author, date}], oldest first
  //   url      the PR's GitHub address, to build each commit's link
  import { openExternal } from '../../api.js'
  import Card from '../ui/Card.svelte'
  import Row from '../ui/Row.svelte'
  import Avatar from '../ui/Avatar.svelte'
  import { ago } from '../ui/format.ts'
  import { commitDays } from './github.ts'
  import { shortSha } from './pr.ts'
  import type { GitHubCommit } from '../api/github.ts'

  let { commits = [], url = '' }: { commits?: GitHubCommit[] | null; url?: string } = $props()
  let days = $derived(commitDays(commits))
  let copyStates = $state<Record<string, 'copying' | 'copied' | 'failed'>>({})
  async function copyHash(sha: string) {
    if (copyStates[sha] === 'copying') return
    copyStates[sha] = 'copying'
    try {
      await navigator.clipboard.writeText(sha)
      copyStates[sha] = 'copied'
    } catch {
      copyStates[sha] = 'failed'
    }
  }
  const commitUrl = (sha: string) => url.replace(/\/pull\/\d+.*$/, `/commit/${sha}`)
</script>

{#each days as day (day.key)}
  <div class="day">
    <Card title="Commits on {day.label}" count={day.commits.length}>
      {#each day.commits as c (c.sha)}
        <Row dense clamp={false} text={c.headline}>
          {#snippet lead()}<Avatar name={c.author?.login || '?'} size={22} />{/snippet}
          {#snippet meta()}<span>{c.author?.name || c.author?.login || 'unknown'}</span><span>{ago(c.date)}</span>{/snippet}
          {#snippet trail()}<span class="commit-actions"><button class="sha" onclick={() => openExternal(commitUrl(c.sha))} title="Open this commit on GitHub" aria-label="Open commit {shortSha(c.sha)} on GitHub">{shortSha(c.sha)}</button><button class="copy" onclick={() => copyHash(c.sha)} disabled={copyStates[c.sha] === 'copying'} aria-label="Copy full hash for commit {shortSha(c.sha)}">Copy</button><span class="copy-status" role="status">{copyStates[c.sha] === 'copied' ? 'Copied' : copyStates[c.sha] === 'failed' ? 'Copy failed. Try again.' : ''}</span></span>{/snippet}
        </Row>
      {/each}
    </Card>
  </div>
{:else}
  <p class="quiet">No commits.</p>
{/each}

<style>
  .day { margin: var(--s3) 0 0; }
  .commit-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 2px 4px; max-width: 150px; flex-shrink: 0; }
  .copy { font-size: var(--t-sm); color: var(--muted); padding: 4px 6px; border-radius: 6px; }
  .copy:hover { background: var(--hover); color: var(--ink); }
  .copy-status { flex-basis: 100%; text-align: right; color: var(--muted); font-size: var(--t-sm); }
  .copy-status:empty { display: none; }
  @media (max-width: 400px) { .commit-actions { max-width: 105px; } }
  .sha { font-family: var(--mono); font-size: var(--t-sm); color: var(--iris); padding: 4px 6px; border-radius: 6px; align-self: center; }
  .sha:hover { background: var(--hover); }
  .quiet { color: var(--faint); font-size: 13.5px; padding: 12px 2px; }
</style>

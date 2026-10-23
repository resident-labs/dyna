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
  const commitUrl = (sha: string) => url.replace(/\/pull\/\d+.*$/, `/commit/${sha}`)
</script>

{#each days as day (day.key)}
  <div class="day">
    <Card title="Commits on {day.label}" count={day.commits.length}>
      {#each day.commits as c (c.sha)}
        <Row dense clamp={false} text={c.headline}>
          {#snippet lead()}<Avatar name={c.author?.login || '?'} size={22} />{/snippet}
          {#snippet meta()}<span>{c.author?.name || c.author?.login || 'unknown'}</span><span>{ago(c.date)}</span>{/snippet}
          {#snippet trail()}<button class="sha" onclick={() => openExternal(commitUrl(c.sha))} title="Open this commit on GitHub" aria-label="Open commit {shortSha(c.sha)} on GitHub">{shortSha(c.sha)}</button>{/snippet}
        </Row>
      {/each}
    </Card>
  </div>
{:else}
  <p class="quiet">No commits.</p>
{/each}

<style>
  .day { margin: var(--s3) 0 0; }
  .sha { font-family: var(--mono); font-size: var(--t-sm); color: var(--iris); padding: 4px 6px; border-radius: 6px; align-self: center; }
  .sha:hover { background: var(--hover); }
  .quiet { color: var(--faint); font-size: 13.5px; padding: 12px 2px; }
</style>

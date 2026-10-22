<script lang="ts">
  // One tracked change as a card: its story (title, one line of facts, the
  // status box), the reviewer's verdict and summary, and its checks. The
  // session's Pull requests tab uses it; the room's PR screen draws the same
  // PRStory and Verdict in its own layouts.
  //
  // Props: the PRStory props, and
  //   showChecks  draw the checks here; the PR screen draws them in a tab
  //   children    optional snippet drawn under the verdict (PR body, comments)
  import { tick } from 'svelte'
  import ChangeDetail from './ChangeDetail.svelte'
  import ChangeReview from './ChangeReview.svelte'
  import PRStory from './PRStory.svelte'
  import Verdict from './Verdict.svelte'
  import PRChecks from './PRChecks.svelte'
  import { statusMeta } from './pr.ts'
  import type { StoryProps } from './props.ts'

  interface Props extends StoryProps {
    showChecks?: boolean
  }
  let { pr, names = {}, showChecks = true, children: more = null, ...story }: Props = $props()
  let meta = $derived(statusMeta(pr))
  let verdictBox = $state<HTMLElement | null>(null)
  let reviewOpen = $state(false)
  async function readReview() {
    reviewOpen = true
    await tick()
    verdictBox?.scrollIntoView({ block: 'start', behavior: 'smooth' })
  }
</script>

<ChangeDetail row={reviewOpen ? pr : null}>
{#snippet children(detail, error, loading, refresh)}
<article class="card {meta.tone}">
  <PRStory {pr} {names} {...story} onread={readReview}>
    {#snippet children()}
      <Verdict pr={{ ...pr, summary: detail?.summary }} {names} bind:el={verdictBox} />
      {#if reviewOpen}
        {#if loading}<p class="line-note">Loading the review…</p>{/if}
        {#if error}<p class="line-note">{error}</p>{/if}
        {#if detail}<ChangeReview change={detail} roomId={pr.roomId} onchange={refresh} />{/if}
      {/if}
      {#if pr.ciTestReportNote}<p class="line-note">{pr.ciTestReportNote}</p>{/if}
      {#if showChecks}<PRChecks checks={pr.checks ?? []} bots={pr.botReviews ?? []} {pr} />{/if}
      {#if more}{@render more()}{/if}
    {/snippet}
  </PRStory>
</article>
{/snippet}
</ChangeDetail>

<style>
  .card { background: var(--side); border: 1px solid var(--line-soft); border-radius: 12px; padding: 14px 16px 12px; }
  .card.coral { border-color: color-mix(in srgb, var(--coral) 30%, var(--line-soft)); }
  .card.gold { border-color: color-mix(in srgb, var(--gold) 30%, var(--line-soft)); }
  .card.sage { border-color: color-mix(in srgb, var(--sage) 30%, var(--line-soft)); }
  .line-note { margin: 0 0 12px; font-size: 13px; color: var(--muted); }
</style>

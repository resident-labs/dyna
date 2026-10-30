<script lang="ts">
  import { sessionStatus } from '../sessions/status.ts'
  import Loading from './Loading.svelte'
  import { dialog as modal } from '../ui/dialog.ts'
  import Avatar from '../ui/Avatar.svelte'
  import CompletionBody from '../ui/CompletionBody.svelte'
  import PostLink from '../ui/PostLink.svelte'
  import Files from '../files/Files.svelte'
  import LoadOlder from '../ui/LoadOlder.svelte'
  import Card from '../ui/Card.svelte'
  import Row from '../ui/Row.svelte'
  import GoalGlyph from '../goals/GoalGlyph.svelte'
  import ExpediteBadge from '../rooms/ExpediteBadge.svelte'
  import { expedite, agentExpedited, goalExpedited } from '../rooms/expedite.ts'
  import { goalAgo, goalParts } from '../goals/goals.ts'
  import { ago, markdown, stamp, tokens, roomLabel } from '../ui/format.ts'
  import StatusPill from '../ui/StatusPill.svelte'
  import { changeName, changeTitle, prAttention, prStory, prTracks, type Facts } from '../prs/pr.ts'

  import type { Attachment, Member, PostDetail, Room } from '../api/room.ts'
  import type { Goal } from '../api/goal.ts'
  import type { MemberProfile } from '../api/session.ts'

  interface Props {
    profile?: MemberProfile | null
    goals?: Goal[]
    // prs are the session's open PRs, newest first.
    prs?: Facts[]
    onpr?: ((number: number) => void) | null
    older?: boolean
    loadingOlder?: boolean
    onolder?: () => void
    error?: string
    room: Room
    ongoal?: ((id: string) => void) | null
    onclose: () => void
    onmessage: (member: Member) => void
    onmention: (member: Member) => void
    ondetail: (detail: PostDetail) => void
    onpost?: ((postId: string) => void) | null
    onopenfile: (attachment: Attachment) => void
  }
  let { profile = null, goals = [], prs = [], onpr = null, older = false, loadingOlder = false, onolder = () => {}, error = '', room, ongoal = null, onclose, onmessage, onmention, ondetail, onpost = null, onopenfile }: Props = $props()

  let m = $derived(profile?.member)
  let held = $derived(goals.filter((g) => !g.shared && (g.holders ?? []).includes(m?.id ?? '')))
  let open = $derived(held.filter((g) => g.state === 'active'))
  let closed = $derived(held.filter((g) => g.state !== 'active').sort((a, b) => (b.done?.at ?? '').localeCompare(a.done?.at ?? '')))
  let showClosed = $state(false)

</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="card" role="dialog" aria-modal="true" aria-label="Member profile" tabindex="-1" use:modal={{ close: onclose }} onclick={(e) => e.stopPropagation()}>
    {#if error}
      <p class="err">{error}</p>
    {:else if !profile}
      <Loading>Loading profile…</Loading>
    {:else}
      {@const m = profile.member}
      <div class="banner bg-{m.hue}">
        <button class="x" onclick={onclose} aria-label="Close">
          <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
        </button>
      </div>
      <div class="ident">
        <Avatar emoji={m.emoji} name={m.name} hue={m.hue} size={80} state={m.state} status={m.status} ring="var(--side)" />
      </div>
      <div class="body">
        <h2 class="hue-{m.hue}">{m.name} {#if agentExpedited($expedite, m)}<ExpediteBadge />{/if}</h2>
        <p class="addr mono">{m.address}</p>
        {#if m.ownerDisplay}<p class="display">uses your display</p>{/if}

        <div class="actions">
          <button class="btn fill lg" onclick={() => onmessage(m)}>Message</button>
          {#if !m.closed}
            <button class="btn lg" onclick={() => onmention(m)}>Mention in #{roomLabel(room)}</button>
          {/if}
        </div>

        <dl class="facts">
          <div><dt>State</dt><dd class="st {m.state}">{sessionStatus(m)}</dd></div>
          <div><dt>Profile</dt><dd>{m.profile}</dd></div>
          <div><dt>Last turn</dt><dd>{m.turns ? `${m.turns} · ${ago(m.lastActivity)}` : 'None yet'}</dd></div>
          <div><dt>Tokens</dt><dd>{tokens((profile.usage?.inputTokens || 0) + (profile.usage?.outputTokens || 0))}</dd></div>
          <div><dt>Parent</dt><dd>{profile.parent || 'root'}</dd></div>
          <div><dt>Started</dt><dd>{stamp(m.created)}</dd></div>
        </dl>

        {#if prs.length}
          <section>
            <Card title="Pull requests" count={prs.length}>
              <div class="goals">
              {#each prs as pr (pr.key ?? pr.changeId)}
                {@const story = prStory(pr)}
                {@const attention = prAttention(pr)}
                {@const n = pr.number}
                <Row text={changeTitle(pr)} onclick={onpr && n ? () => onpr(n) : null}>
                  {#snippet lead()}<span class="num mono">{changeName(pr)}</span>{/snippet}
                  {#snippet detail()}
                    {@const mark = attention && prTracks(pr).ci === 'failed' ? 'ci' : 'review'}
                    <span class="story" class:routine={story.routine}><b>Review</b><span>{#if attention && mark === 'review'}<StatusPill variant="mark" tone={attention.tone} label={attention.label} />{/if}{story.review}</span></span>
                    <span class="story"><b>CI</b><span>{#if attention && mark === 'ci'}<StatusPill variant="mark" tone={attention.tone} label={attention.label} />{/if}{story.ci}</span></span>
                  {/snippet}
                </Row>
              {/each}
              </div>
            </Card>
          </section>
        {/if}

        <section>
          <Card title="Goals" count={open.length}>
            <!-- The room loads recent done goals first, so with older pages the done count is only what is loaded. -->
            {#snippet action()}{#if closed.length || older}<button class="more" onclick={() => (showClosed = !showClosed)}>{showClosed ? 'Hide' : 'Show'} {older ? `done${closed.length ? ` · ${closed.length} loaded` : ''}` : `${closed.length} done`}</button>{/if}{/snippet}
            {#if !open.length}<p class="none">{held.length || older ? 'No active goals.' : 'No goals.'}</p>{/if}
            <div class="goals">
            {#each showClosed ? [...open, ...closed] : open as g (g.id)}
              {@const parts = goalParts(g.text)}
              {@const active = g.state === 'active'}
              <Row text={parts.aim} clamp={!active} context={!active} onclick={ongoal && (() => ongoal(g.id))}>
                {#snippet lead()}<GoalGlyph goal={g} />{/snippet}
                {#snippet meta()}{#if goalExpedited($expedite, g)}<ExpediteBadge />{/if}<span>{g.state}</span><span>{active ? `opened ${goalAgo(g.createdAt)}` : goalAgo(g.done?.at ?? g.updatedAt)}</span>{/snippet}
                {#snippet detail()}
                  {#if !active}{#if g.done?.summary}<span class="when clamp">{g.done.summary}</span>{/if}
                  {:else}{#if parts.done}<span class="when"><b>Done when</b> {parts.done}</span>{/if}{/if}
                {/snippet}
              </Row>
            {/each}
            </div>
            {#if showClosed}<LoadOlder {older} loading={loadingOlder} onclick={onolder} />{/if}
          </Card>
        </section>

        <section>
          <h3>Recent posts</h3>
          {#if !profile.posts?.length}
            <p class="none">No posts in #{roomLabel(room)} yet.</p>
          {/if}
          {#each profile.posts || [] as p (p.id)}
            <article class="post">
              <div class="meta">
                {#if p.completion}
                  <span class="kind {p.kind}">{p.kind === 'failed' ? 'failed' : p.kind === 'stopped' ? 'stopped' : 'finished'} turn {p.turn}</span>
                {:else}
                  <span class="to">to {(p.to || []).map((a) => (a === room.name ? 'root' : a.replace(room.name + ':', ''))).join(', ')}</span>
                {/if}
                <time>{stamp(p.ts)}</time>
                <PostLink roomId={room.id} postId={p.id} onopen={onpost} />
              </div>
              <div class="prose">{#if p.completion}<CompletionBody post={p} trimLead onopen={ondetail} />{:else if p.text}{@html markdown(p.text)}{/if}</div>
              {#if p.attachments?.length}<Files roomId={room.id} postId={p.id} items={p.attachments} onopen={onopenfile} />{/if}
            </article>
          {/each}
        </section>
      </div>
    {/if}
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(8, 8, 10, 0.62);
    display: grid;
    place-items: center;
    z-index: 50;
    padding: var(--s6);
  }
  .card {
    width: min(600px, 100%);
    max-height: min(780px, 100%);
    overflow-y: auto;
    background: var(--side);
    border-radius: 12px;
    box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5);
    outline: none;
    position: relative;
  }
  .err { padding: var(--s5); color: var(--muted); }
  .err { color: var(--coral); }
  .banner { height: 96px; background: color-mix(in srgb, var(--h) 32%, #1c1b1e); position: relative; }
  .x {
    position: absolute;
    top: 12px;
    right: 12px;
    width: 30px;
    height: 30px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    color: var(--ink-2);
    background: rgba(0, 0, 0, 0.35);
    transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease);
  }
  @media (hover: hover) {
    .x:hover { color: var(--ink); background: var(--raise-hover); }
  }
  .x:active {
    background: var(--select);
}
  .ident { padding: 0 20px; margin-top: -40px; height: 88px; }
  .ident :global(.avatar) { box-shadow: 0 0 0 6px var(--side); }
  .body { padding: var(--s2) var(--s5) var(--s5); }
  h2 { margin: 0; font-size: var(--t-display); font-weight: 700; letter-spacing: -0.01em; line-height: 1.2; }
  .addr { margin: 2px 0 0; color: var(--muted); font-size: var(--t-sm); }
  /* The owner-display grant is quiet plain text, not a badge. */
  .display { margin: 2px 0 0; color: var(--faint); font-size: var(--t-sm); }
  .actions { display: flex; gap: var(--s2); margin: var(--s4) 0 var(--s1); }
  dt { font-size: var(--t-sm); font-weight: 600; color: var(--faint); margin-bottom: 2px; }
  dd { margin: 0; font-size: var(--t-body); color: var(--ink-2); }
  .st.live { color: var(--gold); }
  .st.done { color: var(--sage); }
  .st.error { color: var(--coral); }
  section { margin-top: 20px; }
  h3 { margin: 0 0 var(--s2); font-size: var(--t-ui); font-weight: 600; color: var(--muted); }
  .goals { margin: 0 calc(-1 * var(--s3)); }
  .goals:empty { display: none; }
  .facts {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: var(--s3) 20px;
    margin: 18px 0 0;
    padding: var(--s4);
    background: #18171a;
    border-radius: 8px;
  }
  .more { color: var(--muted); font-size: var(--t-sm); transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .more:hover { color: var(--ink); }
  }
  .when { display: block; margin-top: 6px; color: var(--ink-2); font-size: var(--t-ui); line-height: 1.45; overflow-wrap: anywhere; }
  .when.clamp { color: var(--muted); display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  .when b { color: var(--muted); font-weight: 600; margin-right: var(--s1); }
  .num { font-size: var(--t-sm); color: var(--muted); }
  /* A label column keeps wrapped sentences aligned on a phone. */
  .story { display: grid; grid-template-columns: 56px 1fr; margin-top: var(--s1); color: var(--ink-2); font-size: var(--t-ui); line-height: var(--leading); overflow-wrap: anywhere; }
  .story b { color: var(--muted); font-weight: 600; }
  .story.routine { color: var(--muted); }
  .story :global(.mark-only) { vertical-align: -2px; margin-left: -3px; }
  .none { margin: 0; color: var(--faint); font-size: var(--t-body); }
  .post { padding: 10px 0; border-top: 1px solid var(--line-soft); }
  .post:first-of-type { border-top: 0; padding-top: 0; }
  .meta { display: flex; gap: var(--s2); align-items: baseline; font-size: var(--t-sm); color: var(--faint); margin-bottom: var(--s1); }
  .kind { font-weight: 600; color: var(--sage); }
  .kind.failed { color: var(--coral); }
  .kind.stopped { color: var(--muted); }
  .to { color: var(--muted); }
  .post .prose { font-size: var(--t-body); }
</style>

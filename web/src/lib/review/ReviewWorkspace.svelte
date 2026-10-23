<script lang="ts">
  import { untrack, type Snippet } from 'svelte'
  import type { Member } from '../api/room.ts'
  import Tabs from '../ui/Tabs.svelte'
  import ReviewFiles from './ReviewFiles.svelte'
  import ReviewChanges from './ReviewChanges.svelte'

  let { roomId, session = '', sessionName = '', members = [], tab = $bindable('files'), reveal = '', folder = null, version = 0, onpr, onselect, onclose, prs }: {
    roomId: string
    session?: string
    sessionName?: string
    members?: Member[]
    tab?: string
    reveal?: string
    folder?: { path: string } | null
    version?: number
    onpr: (number: number) => void
    onselect: (id: string) => void
    onclose: () => void
    prs: Snippet
  } = $props()
  let width = $state(400)
  // Keep the mounted tabs so switching tabs preserves file position and draft comments.
  let visited = $state(new Set<string>())
  $effect(() => { const selected = tab; untrack(() => { if (!visited.has(selected)) visited = new Set([...visited, selected]) }) })
  const tabs = [{ id: 'files', label: 'Files' }, { id: 'changes', label: 'Changes' }, { id: 'prs', label: 'PRs' }]
</script>

<aside id="review-workspace" bind:clientWidth={width} class="review-workspace" aria-label="Review workspace">
  <header class="workspace-head">
    <span class="heading">Review</span>
    <button class="close" onclick={onclose} aria-label="Close review workspace">Back to chat <span aria-hidden="true">×</span></button>
  </header>
  <Tabs {tabs} bind:tab label="Review views" />
  {#if tab !== 'prs'}
    <label class="workspace-source">Workspace
      <select value={session} onchange={(event) => onselect(event.currentTarget.value)}>
        <option value="">Repository</option>
        {#each members.filter((member) => !member.owner && member.id && (!member.closed || member.id === session) && (member.role !== 'reviewer' || member.id === session)) as member (member.id)}
          <option value={member.id}>{member.name}</option>
        {/each}
      </select>
    </label>
  {/if}
  {#if visited.has('files')}
    <div class="tab-content" hidden={tab !== 'files'}>
      <ReviewFiles {roomId} {session} narrow={width < 680} active={tab === 'files'} {reveal} {folder} {onpr} />
    </div>
  {/if}
  {#if visited.has('changes')}
    <div class="tab-content" hidden={tab !== 'changes'}>
      {#if session}
        <ReviewChanges {roomId} {session} {sessionName} {version} active={tab === 'changes'} />
      {:else}
        <div class="choose"><strong>Review an agent's changes</strong><p>Choose a session above to see its worktree and send line comments to that agent.</p></div>
      {/if}
    </div>
  {/if}
  {#if visited.has('prs')}
    <div class="tab-content" hidden={tab !== 'prs'}>{@render prs()}</div>
  {/if}
</aside>

<style>
  .review-workspace { width: clamp(380px, 48%, 760px); min-width: 0; min-height: 0; flex: none; display: flex; flex-direction: column; background: var(--main); border-left: 1px solid var(--line-soft); }
  .workspace-head { display: flex; align-items: center; justify-content: space-between; gap: var(--s3); padding: var(--s3) var(--s4); border-bottom: 1px solid var(--line-soft); }
  .heading { font-size: var(--t-body); font-weight: 600; }
  .close { color: var(--muted); padding: var(--s2); border-radius: var(--r-xs); font-size: var(--t-sm); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  .close:hover { color: var(--ink); background: var(--hover); }
  .close:active { background: var(--select); }
  .close span { margin-left: var(--s2); font-size: 18px; }
  .workspace-source { display: flex; align-items: center; gap: var(--s3); padding: var(--s3) var(--s4); color: var(--muted); font-size: var(--t-sm); border-bottom: 1px solid var(--line-soft); }
  select { flex: 1; min-width: 0; color: var(--ink); background: var(--raise); border: 1px solid var(--line); border-radius: var(--r-xs); padding: var(--s2); font: inherit; }
  .tab-content { flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column; }
  .tab-content[hidden] { display: none; }
  .choose { margin: auto; padding: var(--s6); max-width: 36ch; text-align: center; }
  .choose p { color: var(--muted); font-size: var(--t-body); }
  @media (max-width: 760px) {
    .review-workspace { position: fixed; inset: 0; width: 100%; z-index: 30; border-left: 0; padding-top: env(safe-area-inset-top); padding-bottom: env(safe-area-inset-bottom); }
    .workspace-head, .workspace-source { min-height: 48px; }
    .close { min-height: 44px; }
  }
</style>

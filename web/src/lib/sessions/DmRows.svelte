<script lang="ts">
  // The rows of a DM transcript. The Messages tab and the Computer tab's chat
  // both render them here. compact narrows the gutter for the chat overlay.
  import Avatar from '../ui/Avatar.svelte'
  import SystemNote from '../ui/SystemNote.svelte'
  import DmMessage from './DmMessage.svelte'
  import ThreadChip from './ThreadChip.svelte'
  import ActivityTimeline from './ActivityTimeline.svelte'
  import CompactionDivider from './CompactionDivider.svelte'
  import { createSelection } from '../ui/selection.svelte.ts'
  import VoiceRelay from '../voice/VoiceRelay.svelte'
  import { rendererFor } from '../rooms/eventRenderers.ts'
  import { done, plain, turnHead, turnTail } from './dm.ts'
  import { stamp, short, ago } from '../ui/format.ts'
  import type { Attachment, Member, Room } from '../api/room.ts'
  import type { ShellJob } from '../api/terminal.ts'
  import type { PostSelection } from '../ui/postSelect.ts'
  import type { DmRow, DmView, Receipt, ThreadPost } from './dm.ts'

  interface Props {
    dm: DmView
    jobs?: ShellJob[]
    onjob?: ((job: ShellJob) => void) | null
    rows?: DmRow[] | null
    room: Room
    onforward?: ((post: ThreadPost) => void) | null
    member: Member
    members?: Member[]
    compact?: boolean
    isNew?: (key: string) => boolean
    selectedKey?: string
    onthread: (key: string) => void
    onopenfile: (attachment: Attachment) => void
    onpost?: ((postId: string) => void) | null
    removing?: string
    onunqueue?: ((pending: Receipt) => void) | null
    ondismiss?: ((pending: Receipt) => void) | null
    onissue?: ((issue: string) => void) | null
    selection?: PostSelection
  }
  let { dm, jobs = [], onjob = null, rows = null, room, onforward = null, member, members = [], compact = false, isNew = () => false, selectedKey = '', onthread, onopenfile, onpost = null, removing = '', onunqueue = null, ondismiss = null, onissue = null, selection = createSelection() }: Props = $props()
  // selection is shared by every message in the list (postSelect.ts); a
  // caller that renders one row at a time passes one shared selection.
  let rendered = $derived(rows ?? dm.rows)
  let liveKey = $derived.by(() => {
    if (member.state !== 'live') return ''
    const turn = dm.rows.findLast((r) => r.kind === 'turn')
    return dm.rows.findLast((r) => r.kind === 'timeline' && r.turn?.n === turn?.turn.n)?.key ?? ''
  })
  const turnText: Record<string, string> = { ok: 'finished', error: 'failed', stopped: 'stopped', running: 'running', queued: 'queued' }
</script>

<div class="rows" class:compact>
  {#each rendered as row (row.key)}
    {#if row.kind === 'turn'}
      {@const t = row.turn}
      <div class="rule" id="turn-{t.n}"><span>{turnHead(t)} · {stamp(t.started)}{#if !done.includes(t.status ?? '')}{' · '}<b class={t.status}>{turnText[t.status ?? ''] || t.status}</b>{/if}</span></div>
    {:else if row.kind === 'turnEnd'}
      <p class="turn-end" class:enter={isNew(row.key)}><b class={row.turn.status}>{turnText[row.turn.status ?? ''] || row.turn.status}</b>{turnTail(row.turn) ? ` · ${turnTail(row.turn)}` : ''}</p>
    {:else if row.kind === 'message'}
      <DmMessage {row} {room} {onforward} {member} address={dm.address} {members} {compact} {onopenfile} {onpost} {removing} {onunqueue} {ondismiss} {selection} enter={isNew(row.key)} />
    {:else if row.kind === 'system-event'}
      {@const Card = rendererFor(row.post)}
      <div class="event-row" class:enter={isNew(row.key)}>
        {#if Card}
          <Card post={row.post} notice={row.post.notice} surface="dm" {room} onissue={onissue ?? undefined} />
        {:else}
          <p class="note event-note">Unknown event {row.post.kind}/{row.post.notice?.kind ?? '?'}</p>
        {/if}
      </div>
    {:else if row.kind === 'voice'}
      <VoiceRelay icon="mic" label="Sent by Voice for you" text={row.text} state={row.delivery === 'read' ? '' : row.delivery || ''} />
    {:else if row.kind === 'timeline' || row.kind === 'thought'}
      <div class="activity-row" class:cont={row.cont} class:enter={isNew(row.key)}>
        <div class="activity-gutter">{#if !row.cont}<Avatar name={member.name} hue={member.hue} emoji={member.emoji} size={compact ? 28 : 40} />{/if}</div>
        <div class="activity-body">
          {#if !row.cont}<div class="activity-head"><strong class="hue-{member.hue}">{member.name}</strong><time>{stamp(row.ts)}</time></div>{/if}
          {#if row.kind === 'timeline'}
            <ActivityTimeline {row} {jobs} {onjob} {onthread} doing={row.key === liveKey ? member.doing : undefined} />
          {:else}<p class="thought">{row.text}{#if row.aside}<span class="aside">{row.aside}</span>{/if}</p>{/if}
        </div>
      </div>
    {:else if row.kind === 'thread'}
      {@const thread = row.thread}
      {@const person = members.find((p) => p.address === thread.key)}
      <div class="thread-row">
        <ThreadChip glyph="▱" name={short(thread.key, room.name)} emoji={person?.emoji} hue={person?.hue} badge={`${row.posts.length} message${row.posts.length === 1 ? '' : 's'}`} queued={row.queued} snippet={plain(row.posts.at(-1)?.text)} time={ago(row.posts.at(-1)?.ts)} selected={selectedKey === thread.key} enter={isNew(row.key)} onopen={() => onthread(thread.key)} />
      </div>
    {:else if row.kind === 'instructions'}
      <SystemNote notes={row.notes ?? [{ ...row.note, id: row.note?.id ?? row.key, target: member.name, text: row.text, ts: row.ts }]} roomId={room.id} {onopenfile} />
    {:else if row.kind === 'compaction'}
      <CompactionDivider {row} enter={isNew(row.key)} />
    {:else}<p class:error={row.kind === 'error'} class="note event-note">{row.text}</p>{/if}
  {/each}
</div>

<style>
  .rows { --gutter: 56px; }
  .rows.compact { --gutter: 40px; }
  .rule {
    display: flex;
    align-items: center;
    justify-content: center;
    border-top: 1px solid var(--line);
    margin: 26px var(--s4) var(--s3);
    height: 0;
  }
  .rule span {
    background: var(--main);
    padding: 0 var(--s2);
    font-size: var(--t-sm);
    color: var(--faint);
    transform: translateY(-50%);
  }
  .turn-end { margin: 6px var(--s4) 0 var(--gutter); font-size: var(--t-cap); color: var(--faint); }
  :is(.rule, .turn-end) b { font-weight: 600; }
  :is(.rule, .turn-end) b.ok { color: var(--sage); }
  :is(.rule, .turn-end) b.error { color: var(--coral); }
  .rule b.running, .rule b.queued { color: var(--gold); }
  .thought { margin: var(--s1) 0; font-size: var(--t-ui); font-style: italic; color: var(--faint); }
  .thought .aside { margin-left: var(--s2); font-style: normal; }
  .error { margin: 6px 0; font-size: var(--t-ui); color: var(--coral); }
  .event-note.error { color: var(--coral); }
  .note { margin: var(--s1) 0; font-size: var(--t-sm); color: var(--faint); }
  .activity-row { display: grid; grid-template-columns: var(--gutter) minmax(0, 1fr); padding: 2px var(--s4) 2px 0; margin-top: 14px; }
  .activity-row.cont { margin-top: 0; }
  .activity-gutter { display: flex; justify-content: center; padding-top: 2px; }
  .activity-body { min-width: 0; }
  .activity-head { display: flex; align-items: baseline; gap: var(--s2); font-size: var(--t-body); }
  .activity-head time { font-size: var(--t-cap); color: var(--faint); }
  .event-row { margin: 6px var(--s4) 2px var(--gutter); }
  .thread-row { display: flex; margin: 6px var(--s4) 2px var(--gutter); }
  .thread-row :global(.thread-chip) { flex: 1; }
  .event-note { margin-left: var(--gutter); }
  .compact .thread-row :global(.snippet), .compact .thread-row :global(time) { display: none; }
  @media (max-width: 760px) {
    .thread-row :global(.snippet), .thread-row :global(time) { display: none; }
    .thread-row :global(.thread-chip) { flex-wrap: wrap; }
    .rule span { font-size: var(--t-cap); }
  }
</style>

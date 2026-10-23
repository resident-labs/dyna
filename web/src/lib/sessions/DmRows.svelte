<script lang="ts">
  // The rows of a DM transcript. The Messages tab and the Computer tab's chat
  // both render them here. compact narrows the gutter for the chat overlay.
  import Avatar from '../ui/Avatar.svelte'
  import SystemNote from '../ui/SystemNote.svelte'
  import DmMessage from './DmMessage.svelte'
  import FailedCall from './FailedCall.svelte'
  import ShellBlock from './ShellBlock.svelte'
  import ShellGroup from './ShellGroup.svelte'
  import CompactionDivider from './CompactionDivider.svelte'
  import { mergeJob } from './shellBlock.ts'
  import { createSelection } from '../ui/selection.svelte.ts'
  import VoiceRelay from '../voice/VoiceRelay.svelte'
  import { activitySummary, done, plain, turnHead, turnTail } from './dm.ts'
  import { stamp, short, ago, elapsed, isLongCall } from '../ui/format.ts'
  import type { Attachment, Member, Room } from '../api/room.ts'
  import type { ShellJob } from '../api/terminal.ts'
  import type { PostSelection } from '../ui/postSelect.ts'
  import type { DmRow, DmView, Receipt, ThreadPost } from './dm.ts'

  interface Props {
    dm: DmView
    jobs?: ShellJob[]
    onjob?: ((job: ShellJob) => void) | null
    rows?: DmRow[] | null
    offset?: number
    room: Room
    onforward?: ((post: ThreadPost) => void) | null
    member: Member
    members?: Member[]
    compact?: boolean
    isNew?: (key: string) => boolean
    selectedKey?: string
    onthread: (key: string) => void
    onopenfile: (attachment: Attachment) => void
    onpost?: ((post: ThreadPost) => void) | null
    removing?: string
    onunqueue?: ((pending: Receipt) => void) | null
    ondismiss?: ((pending: Receipt) => void) | null
    onissue?: ((number: number) => void) | null
    selection?: PostSelection
  }
  let { dm, jobs = [], onjob = null, rows = null, offset = 0, room, onforward = null, member, members = [], compact = false, isNew = () => false, selectedKey = '', onthread, onopenfile, onpost = null, removing = '', onunqueue = null, ondismiss = null, onissue = null, selection = createSelection() }: Props = $props()
  // selection is shared by every message in the list (postSelect.ts); a
  // caller that renders one row at a time passes one shared selection.
  let targets = $state<Record<string, boolean>>({})
  let rendered = $derived(rows ?? dm.rows)
  // While the agent works, the newest activity row shows the member's live
  // activity (the same one the members list shows) next to the collapsed
  // block, with an elapsed time while a call runs. It belongs to the running
  // turn only, and goes with it.
  let liveKey = $derived.by(() => {
    if (member.state !== 'live') return ''
    const turn = dm.rows.findLastIndex((r) => r.kind === 'turn')
    const act = dm.rows.findLastIndex((r) => r.kind === 'activity')
    return act > turn ? dm.rows[act]?.key ?? '' : ''
  })
  let now = $state(Date.now())
  $effect(() => {
    if (!liveKey) return
    const timer = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(timer)
  })
  function liveLine() {
    const doing = member.doing
    return { text: doing?.text ?? '', ms: doing?.since ? now - doing.since : 0 }
  }
  const turnText: Record<string, string> = { ok: 'finished', error: 'failed', stopped: 'stopped', running: 'running', queued: 'queued' }
</script>

<div class="rows" class:compact>
  {#each rendered as row, i (row.key)}
    {#if row.kind === 'turn'}
      {@const t = row.turn}
      <div class="rule" id="turn-{t.n}"><span>{turnHead(t)} · {stamp(t.started)}{#if !done.includes(t.status ?? '')}{' · '}<b class={t.status}>{turnText[t.status ?? ''] || t.status}</b>{/if}{#if row.wokeBy}{@const by = row.wokeBy}{@const woke = mergeJob(by, jobs.find((j) => j.id === by.id))}{' · woke by '}<button class="lnk" onclick={() => document.getElementById('shell-' + woke.id)?.scrollIntoView({ block: 'center' })}>{woke.name || 'a job'} ↑</button>{#if woke.code}{' · '}<b class="error">exit {woke.code}</b>{/if}{/if}</span></div>
    {:else if row.kind === 'turnEnd'}
      <p class="turn-end" class:enter={isNew(row.key)}><b class={row.turn.status}>{turnText[row.turn.status ?? ''] || row.turn.status}</b>{turnTail(row.turn) ? ` · ${turnTail(row.turn)}` : ''}</p>
    {:else if row.kind === 'message'}
      <DmMessage {row} {room} {onforward} {member} address={dm.address} {members} {compact} {onopenfile} {onpost} {removing} {onunqueue} {ondismiss} {selection} enter={isNew(row.key)} />
    {:else if row.kind === 'voice'}
      <VoiceRelay icon="mic" label="Sent by Voice for you" text={row.text} state={row.delivery === 'read' ? '' : row.delivery || ''} />
    {:else if row.kind === 'activity' || row.kind === 'thought' || row.kind === 'shell' || row.kind === 'shellGroup' || row.kind === 'shellNote'}
      <div class="activity-row" class:cont={row.cont} class:enter={isNew(row.key)}>
        <div class="activity-gutter">{#if !row.cont}<Avatar name={member.name} hue={member.hue} emoji={member.emoji} size={compact ? 28 : 40} />{/if}</div>
        <div class="activity-body">
        {#if !row.cont}<div class="activity-head"><strong class="hue-{member.hue}">{member.name}</strong><time>{stamp(row.ts)}</time></div>{/if}
        {#if row.kind === 'shellGroup'}
          <ShellGroup jobs={row.jobs.map((j) => mergeJob(j, jobs.find((l) => l.id === j.id)))} {onjob} />
        {:else if row.kind === 'shell'}
          <ShellBlock job={mergeJob(row.job, jobs.find((j) => j.id === row.job.id))} finished={row.finished} {onjob} />
        {:else if row.kind === 'shellNote'}
          {@const noted = mergeJob(row.job, jobs.find((j) => j.id === row.job.id))}
          <p class="ptr" id="note-{row.job.id}"><span aria-hidden="true">↳</span><button class="lnk" onclick={() => document.getElementById('shell-' + row.job.id)?.scrollIntoView({ block: 'center' })}>{noted.name || 'A job'} finished ↑</button><span>· exit {noted.code}</span></p>
        {:else if row.kind === 'thought'}
          <p class="thought">{row.text}{#if row.aside}<span class="aside">{row.aside}</span>{/if}</p>
        {:else if row.kind === 'activity'}
        <details class="activity">
        <summary class:failed={row.items.some((it) => it.failed)}>{activitySummary(row.items, dm.rows[offset + i + 1]?.ts)}{#if row.key === liveKey}{@const live = liveLine()}<span class="live" class:long={isLongCall(live.ms)} role="status">{live.text}{#if live.ms}{' · ' + elapsed(live.ms)}{/if}</span>{/if}</summary>
        <div class="activity-list">
          {#each row.items as it, index (it.key || index)}
              {@const targetKey = JSON.stringify([row.key, it.key || index])}
              <div class="tool act-line" class:failed={it.failed}><span class="verb">{it.tool}</span>{#if it.target}<button class="target-toggle" aria-expanded={!!targets[targetKey]} aria-label={`${targets[targetKey] ? 'Hide' : 'Show'} full ${it.tool || 'tool'} target`} onclick={() => targets[targetKey] = !targets[targetKey]}><code title={it.target}>{it.target}</code><span class="target-chevron" aria-hidden="true">›</span></button>{/if}{#if it.aside}<span class="aside">{it.aside}</span>{/if}</div>
              {#if it.target && targets[targetKey]}<pre class="target-detail">{it.target}</pre>{/if}
              {#if it.failed}<FailedCall command={it.command} output={it.output} exit={it.exit} />{/if}
          {/each}
        </div>
      </details>
      {/if}
      </div></div>
    {:else if row.kind === 'thread' && row.shell && jobs.some((j) => j.id === row.shell)}
      <!-- The call is not on this page: the notice is the job's own block. -->
      {@const own = jobs.find((j) => j.id === row.shell)}
      <div class="activity-row" class:enter={isNew(row.key)}>
        <div class="activity-gutter"></div>
        <div class="activity-body">{#if own}<ShellBlock job={own} {onjob} />{/if}</div>
      </div>
    {:else if row.kind === 'thread'}
      {@const thread = row.thread}
      {@const person = members.find((p) => p.address === thread.key)}
      <button class="thread-chip" class:enter={isNew(row.key)} class:selected={selectedKey === thread.key} onclick={() => onthread(thread.key)}>
        <span aria-hidden="true">▱</span><Avatar name={short(thread.key, room.name)} emoji={person?.emoji} hue={person?.hue} size={18} />
        <strong>{short(thread.key, room.name)}</strong><b>{row.posts.length} message{row.posts.length === 1 ? "" : "s"}</b>{#if row.queued}<span class="queued">queued</span>{/if}
        <span class="snippet">{plain(row.posts.at(-1)?.text)}</span><time>{ago(row.posts.at(-1)?.ts)} ›</time>
      </button>
    {:else if row.kind === 'instructions'}
      <SystemNote notes={row.notes ?? [{ ...row.note, id: row.note?.id ?? row.key, target: member.name, text: row.text, ts: row.ts }]} roomId={room.id} {onopenfile} {onissue} />
    {:else if row.kind === 'compaction'}
      <CompactionDivider {row} enter={isNew(row.key)} />
    {:else}<p class:error={row.kind === 'error'} class="note event-note">{row.text}</p>{/if}
  {/each}
</div>

<style>
  .live.long { color: var(--gold); font-weight: 600; }
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
  .fallback { color: var(--gold); padding: var(--s2) var(--s4); margin: 0; font-size: var(--t-ui); border-bottom: 1px solid var(--line-soft); }
  .tool { display: flex; align-items: baseline; gap: var(--s2); padding: 2px 0; min-width: 0; max-width: 920px; }
  .tool .verb { flex: none; }
  .tool code { color: var(--ink-2); background: none; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; font: inherit; }
  .target-toggle { display: flex; align-items: baseline; gap: var(--s1); min-width: 0; padding: 0; text-align: left; color: inherit; }
  .target-chevron { flex: none; color: var(--faint); }
  .target-toggle[aria-expanded="true"] .target-chevron { transform: rotate(90deg); }
  .target-toggle:focus-visible { outline-offset: 2px; }
  .target-detail { max-width: 920px; margin: var(--s1) 0 var(--s2); padding: var(--s2); border-radius: var(--r-xs); background: var(--rail); white-space: pre-wrap; overflow-wrap: anywhere; font: var(--t-sm)/1.6 var(--mono); color: var(--ink-2); user-select: text; }
  .tool .aside { flex: none; color: var(--faint); }
  .tool.failed .verb, .tool.failed .aside { color: var(--coral); }
  .ptr { display: flex; gap: var(--s2); align-items: baseline; margin: 2px 0; font-size: var(--t-sm); color: var(--faint); }
  .lnk { background: none; border: 0; padding: 0; color: var(--iris); font: inherit; cursor: pointer; }
  .lnk:hover { text-decoration: underline; }
  .thought { margin: var(--s1) 0; font-size: var(--t-ui); font-style: italic; color: var(--faint); }
  .thought .aside { margin-left: var(--s2); font-style: normal; }
  .error { margin: 6px 0; font-size: var(--t-ui); color: var(--coral); }
  .note { margin: var(--s1) 0; font-size: var(--t-sm); color: var(--faint); }
  .activity-row { display: grid; grid-template-columns: var(--gutter) minmax(0, 1fr); padding: 2px var(--s4) 2px 0; margin-top: 14px; }
  .activity-row.cont { margin-top: 0; }
  .activity-gutter { display: flex; justify-content: center; padding-top: 2px; }
  .activity-body { min-width: 0; }
  .activity-head { display: flex; align-items: baseline; gap: var(--s2); font-size: var(--t-body); }
  .activity-head time { font-size: var(--t-cap); color: var(--faint); }
  .activity { margin: var(--s1) 0; color: var(--muted); font-size: var(--t-sm); }
  .activity summary { cursor: pointer; width: fit-content; max-width: 100%; padding: var(--s1) 10px; background: var(--side); border: 1px solid var(--line-soft); border-radius: 6px; }
  .activity summary.failed, .event-note.error { color: var(--coral); }
  .live { margin-left: var(--s2); color: var(--ink-2); }
  .activity-list { border-left: 2px solid var(--line); margin-top: var(--s1); padding-left: 10px; }
  .thread-chip { display: flex; align-items: center; gap: var(--s2); margin: 6px var(--s4) 2px var(--gutter); padding: 6px 10px; border: 1px solid var(--line-soft); border-radius: 8px; background: var(--side); max-width: calc(100% - var(--gutter) - 16px); font-size: var(--t-ui); text-align: left; }
  .thread-chip b { color: var(--iris); white-space: nowrap; }
  .thread-chip strong { white-space: nowrap; }
  .thread-chip .queued { color: var(--gold); font-size: var(--t-cap); white-space: nowrap; }
  .thread-chip time { color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .snippet { color: var(--faint); flex: 1; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; min-width: 0; }
  .thread-chip.selected { border-color: var(--iris-edge); background: var(--iris-wash); }
  .event-note { margin-left: var(--gutter); }
  .compact .thread-chip .snippet, .compact .thread-chip time { display: none; }
  @media (max-width: 760px) {
    .thread-chip .snippet, .thread-chip time { display: none; }
    .thread-chip { flex-wrap: wrap; }
    .rule span { font-size: var(--t-cap); }
  }
</style>

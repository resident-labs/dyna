<script lang="ts">
  // The review side of a change: the author's notes, the review rounds, and
  // the findings grouped by severity with their replies. Authors answer
  // reviewer findings; the owner can only resolve their own comments. PR details
  // draws it as its extra section, for a local change or a PR.
  //
  // Props:
  //   change    a Changes row
  //   roomId    the room, for ReplyToFinding; without it nothing resolves
  //   onchange  reload Changes after a reply
  import { api } from '../../api.js'
  import { ago } from '../ui/format.ts'
  import { groupFindings, dispositionMeta, verdictMeta, shortSha } from './pr.ts'
  import type { AuthorNotes, Finding } from '../api/change.ts'
  import type { ReviewChange } from './github.ts'
  import { errorText } from '../errorText.ts'

  interface Props {
    change: ReviewChange
    roomId?: string
    onchange?: () => void | Promise<void>
  }
  let { change, roomId = '', onchange = () => {} }: Props = $props()

  const NOTES: { key: keyof AuthorNotes; label: string }[] = [
    { key: 'intent', label: 'Intent' },
    { key: 'testing', label: 'Testing' },
    { key: 'risks', label: 'Risks' },
    { key: 'questions', label: 'Questions for the reviewer' },
  ]
  let notes = $derived(NOTES.filter((n) => change.notes?.[n.key]?.trim()))
  let groups = $derived(groupFindings(change.findings))
  let rounds = $derived([...(change.reviewRounds ?? [])].sort((a, b) => b.round - a.round))
  let openRound = $state(0)
  let patches = $state(new Set<string>())
  let busy = $state('') // the finding id being resolved
  let error = $state({ id: '', text: '' }) // the last failed resolve

  function togglePatch(id: string) {
    const next = new Set(patches)
    next.has(id) ? next.delete(id) : next.add(id)
    patches = next
  }

  async function resolve(f: Finding) {
    busy = f.id
    error = { id: '', text: '' }
    try {
      await api.replyToFinding(roomId, change.changeId, f.id, 'resolved', 'Resolved by the owner')
      await onchange()
    } catch (e) {
      error = { id: f.id, text: errorText(e) }
    } finally {
      busy = ''
    }
  }

  // A patch line reads as added, removed or context.
  const lineTone = (l: string) => (l.startsWith('+') && !l.startsWith('+++') ? 'add' : l.startsWith('-') && !l.startsWith('---') ? 'del' : '')
</script>

{#snippet finding(f: Finding, replies = true)}
  <li class="finding">
    <div class="fh">
      <span class="where mono">{f.file}{f.line ? `:${f.line}` : ''}</span>
    </div>
    {#if f.why}<p class="why">{f.why}</p>{/if}
    {#if f.fix}<p class="fix"><strong>Fix</strong> {f.fix}</p>{/if}
    {#if f.patch}
      <button class="mini" aria-expanded={patches.has(f.id)} onclick={() => togglePatch(f.id)}>{patches.has(f.id) ? 'Hide patch' : 'Show patch'}</button>
      {#if patches.has(f.id)}<pre class="patch mono">{#each f.patch.split('\n') as l}<span class={lineTone(l)}>{l}
</span>{/each}</pre>{/if}
    {/if}
    {#if replies}
      {#if f.replies?.length}
        <ul class="replies" aria-label="Replies">
          {#each f.replies as r}
            {@const dm = dispositionMeta(r.disposition)}
            <li>
              <span class="rw"><span class="disp {dm.tone}">{dm.label}</span><span class="by">{r.author || 'author'}{r.head ? ` at ${shortSha(r.head)}` : ''}{r.at ? ` · ${ago(r.at)}` : ''}</span></span>
              {#if r.message}<span class="rmsg">{r.message}</span>{/if}
            </li>
          {/each}
        </ul>
      {/if}
      {#if roomId && f.severity === 'owner'}
        <button class="mini" onclick={() => resolve(f)} disabled={!!busy}>{busy === f.id ? 'Resolving…' : 'Resolve'}</button>
        {#if error.id === f.id}<p class="err">{error.text}</p>{/if}
      {/if}
    {/if}
  </li>
{/snippet}

{#if notes.length}
  <h4>Author's notes</h4>
  <dl class="notes">
    {#each notes as n}<div><dt>{n.label}</dt><dd>{change.notes?.[n.key]}</dd></div>{/each}
  </dl>
{/if}

{#if groups.length}
  {#each groups as g (g.id)}
    <h4 class="sev">
      <span class="gdot {g.tone}" aria-hidden="true"></span>{g.label}<span>{g.items.length}</span>
    </h4>
    <ul class="findings">
      {#each g.items as f (f.id)}{@render finding(f)}{/each}
    </ul>
  {/each}
{/if}

{#if rounds.length}
  <h4>Review rounds<span>{rounds.length}</span></h4>
  <ul class="rounds">
    {#each rounds as r (r.round)}
      {@const vm = verdictMeta(r.verdict)}
      <li>
        <button class="rhit" aria-expanded={openRound === r.round} onclick={() => (openRound = openRound === r.round ? 0 : r.round)}>
          <span class="rn">Round {r.round}</span>
          <span class="rv {vm?.tone}">{vm ? `${vm.label}${r.verdict === 'approve-with-nits' ? ' with nits' : ''}` : 'No verdict'}</span>
          <span class="rm">{[r.head ? `at ${shortSha(r.head)}` : '', r.findings?.length ? `${r.findings.length} ${r.findings.length === 1 ? 'finding' : 'findings'}` : '', r.at ? ago(r.at) : ''].filter(Boolean).join(' · ')}</span>
        </button>
        {#if openRound === r.round}
          <div class="rbody">
            {#if r.summary}<p class="rsum">{r.summary}</p>{/if}
            {#each groupFindings(r.findings) as g (g.id)}
              <h5><span class="gdot {g.tone}" aria-hidden="true"></span>{g.label}<span>{g.items.length}</span></h5>
              <ul class="findings plain">{#each g.items as f (f.id)}{@render finding(f, false)}{/each}</ul>
            {/each}
          </div>
        {/if}
      </li>
    {/each}
  </ul>
{/if}

<style>
  h4 { display: flex; gap: 8px; align-items: center; margin: 12px 0 6px; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: 0.06em; }
  h4 span:not(.gdot) { font-weight: 500; letter-spacing: 0; color: var(--faint); text-transform: none; }
  h5 { display: flex; gap: 6px; align-items: center; margin: 8px 0 4px; font-size: 12px; font-weight: 600; color: var(--muted); }
  h5 span:not(.gdot) { font-weight: 500; color: var(--faint); }
  .gdot { width: 8px; height: 8px; border-radius: 50%; background: var(--faint); flex: none; }
  .gdot.coral { background: var(--coral); }
  .gdot.gold { background: var(--gold); }
  .gdot.iris { background: var(--iris); }
  .notes { margin: 0 0 10px; display: flex; flex-direction: column; gap: 8px; background: var(--main); border-radius: 8px; padding: 10px 12px; }
  .notes dt { font-size: 12px; font-weight: 600; color: var(--muted); }
  .notes dd { margin: 2px 0 0; font-size: 13.5px; color: var(--ink-2); white-space: pre-wrap; overflow-wrap: anywhere; }
  .findings { list-style: none; margin: 0 0 10px; padding: 0; border: 1px solid var(--line-soft); border-radius: 8px; overflow: hidden; }
  .findings.plain { border: 0; border-radius: 0; margin: 0; }
  .finding { padding: 8px 10px; display: flex; flex-direction: column; gap: 4px; align-items: flex-start; }
  .finding + .finding { border-top: 1px solid var(--line-soft); }
  .where { font-size: 12px; color: var(--ink-2); overflow-wrap: anywhere; }
  .why, .fix { margin: 0; font-size: 13.5px; color: var(--ink-2); overflow-wrap: anywhere; }
  .fix strong { color: var(--muted); font-size: 12px; margin-right: 4px; }
  .patch { margin: 0; align-self: stretch; overflow-x: auto; background: var(--main); border-radius: 6px; padding: 8px 10px; font-size: 12px; line-height: 1.5; }
  .patch span { display: block; white-space: pre; color: var(--muted); }
  .patch .add { color: var(--sage); }
  .patch .del { color: var(--coral); }
  .replies { list-style: none; margin: 2px 0 0; padding: 0 0 0 10px; border-left: 2px solid var(--line); align-self: stretch; display: flex; flex-direction: column; gap: 6px; }
  .replies li { display: flex; flex-direction: column; gap: 1px; }
  .rw { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 8px; }
  .disp { font-size: 12px; font-weight: 700; color: var(--muted); }
  .disp.sage { color: var(--sage); }
  .disp.gold { color: var(--gold); }
  .by { font-size: 12px; color: var(--faint); }
  .rmsg { font-size: 13.5px; color: var(--ink-2); white-space: pre-wrap; overflow-wrap: anywhere; }
  .err { margin: 0; color: var(--coral); font-size: 12.5px; }
  .mini { font-size: 12.5px; font-weight: 600; color: var(--ink-2); border-radius: 6px; padding: 4px 10px; background: var(--raise); }
  .mini:hover:not(:disabled) { color: var(--ink); background: var(--raise-hover); }
  button:disabled { opacity: 0.5; cursor: default; }
  .line-note { margin: 8px 0; font-size: 13px; color: var(--muted); }
  .rounds { list-style: none; margin: 0 0 10px; padding: 0; border: 1px solid var(--line-soft); border-radius: 8px; overflow: hidden; }
  .rounds li + li { border-top: 1px solid var(--line-soft); }
  .rhit { display: flex; align-items: baseline; flex-wrap: wrap; gap: 2px 10px; width: 100%; text-align: left; padding: 7px 10px; font-size: 13px; }
  .rhit:hover { background: var(--hover); }
  .rn { font-weight: 600; color: var(--ink-2); }
  .rv { font-weight: 600; color: var(--muted); }
  .rv.ok { color: var(--sage); }
  .rv.warn { color: var(--gold); }
  .rm { font-size: 12px; color: var(--faint); }
  .rbody { padding: 0 10px 8px; }
  .rsum { margin: 0 0 4px; font-size: 13.5px; color: var(--ink-2); white-space: pre-wrap; overflow-wrap: anywhere; }
  @media (max-width: 760px) {
    .mini { min-height: 44px; padding: 0 14px; }
    .finding { flex-direction: row; flex-wrap: wrap; align-items: center; }
    .finding > :not(.mini) { flex: 0 0 100%; }
    .rhit { min-height: 44px; }
  }
</style>

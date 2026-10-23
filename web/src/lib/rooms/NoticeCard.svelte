<script lang="ts">
  // The Home card for a machine notice: the low disk, a provider limit or a
  // remote history split. The title and the facts come from the notice's kind,
  // never from a stored sentence, and each kind offers the one place the owner
  // can go about it. A notice saved before the typed facts shows its line.
  import { formatBytes } from '../settings/display.ts'
  import { ago } from '../ui/format.ts'
  import { resetLine } from './noticeTime.ts'
  import type { Notice } from '../api/settings.ts'

  interface Props {
    notice: Notice
    // action is where the owner can go about this kind, when the app knows a
    // place for it.
    action?: { label: string; run: () => void } | null
    ondismiss: () => void
  }
  let { notice, action = null, ondismiss }: Props = $props()

  const titles: Record<Notice['kind'], string> = {
    disk: 'Low disk space',
    provider: 'Provider limit reached',
    sync: 'Remote history split',
  }

  let title = $derived(notice.kind === 'sync' && notice.sync?.rewritten ? 'Remote history rewritten' : titles[notice.kind] ?? 'Dyna alert')
  let tone = $derived(notice.severity === 'error' ? 'coral' : 'gold')

  // resets says how long a provider window has left, counting from now.
  let resets = $derived.by(() => {
    const at = notice.provider?.resetsAt
    if (!at) return ''
    const ms = new Date(at).getTime()
    if (!Number.isFinite(ms)) return ''
    return resetLine(ms - Date.now())
  })
</script>

<div class="alert-card" class:error={notice.severity === 'error'} data-id={notice.id}>
  <div class="head">
    <span class="dot {tone}" aria-hidden="true"></span>
    <span class="title">{title}</span>
    <time title={new Date(notice.at).toLocaleString()}>{ago(notice.at)}</time>
    <button class="x" aria-label="Dismiss" title="Dismiss" onclick={ondismiss}>×</button>
  </div>
  {#if notice.kind === 'disk' && notice.disk}
    <ul class="facts">
      {#each notice.disk.paths as p (p.path)}
        <li><span class="where">{p.path}</span> <span class="value">{formatBytes(p.freeBytes)} free</span></li>
      {/each}
    </ul>
    <p class="sub">
      {#if notice.disk.freedBytes}<span>Maintenance freed {formatBytes(notice.disk.freedBytes)}.</span>{/if}
      {#if notice.disk.kept?.length}<span>Kept {notice.disk.kept.join('; ')}.</span>{/if}
      {#if notice.disk.maintenanceFailed}<span class="bad">Some maintenance failed; see the host log.</span>{/if}
    </p>
  {:else if notice.kind === 'provider' && notice.provider}
    <p class="facts one"><span class="where">{notice.provider.profile}</span> {#if resets}<span class="value">{resets}</span>{/if}</p>
  {:else if notice.kind === 'sync' && notice.sync}
    <p class="facts one">
      <span class="where">{notice.sync.repo}</span>
      {' '}
      <span class="value">{notice.sync.rewritten ? `${notice.sync.lane} was rewritten` : `${notice.sync.lane} and ${notice.sync.other ?? 'another remote'} no longer share a history`}</span>
    </p>
  {:else}
    <!-- TODO: legacy/shim remove later — a notice saved before the typed
         facts has only its line. Remove when that history ages out. -->
    <p class="sub legacy">{notice.text}</p>
  {/if}
  <div class="acts">
    {#if action}<button class="btn" type="button" onclick={() => action.run()}>{action.label}</button>{/if}
    {#if notice.command}<code class="mono">{notice.command}</code>{/if}
  </div>
</div>

<style>
  .alert-card {
    padding: 8px 0 8px var(--s2);
    border-left: 2px solid var(--gold);
  }
  .alert-card.error { border-left-color: var(--coral); }
  .head { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .dot { flex: none; width: 7px; height: 7px; border-radius: 50%; }
  .dot.coral { background: var(--coral); }
  .dot.gold { background: var(--gold); }
  .title { flex: 1 1 auto; min-width: 0; font-weight: 600; color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  time { flex: none; color: var(--faint); font-size: var(--t-cap); }
  .x { flex: none; color: var(--faint); font-size: var(--t-ui); line-height: 1; padding: 0 4px; transition: color var(--motion-fast) var(--ease); }
  .x:hover { color: var(--ink-2); }
  .facts { margin: 4px 0 0; padding: 0; list-style: none; display: grid; gap: 2px; font-size: var(--t-sm); }
  .facts.one { display: flex; gap: 8px; flex-wrap: wrap; }
  .where { color: var(--ink-2); font-family: var(--mono); overflow-wrap: anywhere; }
  .value { color: var(--muted); }
  .sub { margin: 4px 0 0; display: flex; flex-wrap: wrap; gap: 4px 10px; color: var(--faint); font-size: var(--t-sm); }
  .sub.legacy { color: var(--muted); }
  .bad { color: var(--coral); }
  .acts { margin-top: 6px; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .mono { color: var(--muted); font: var(--t-sm)/1.5 var(--mono); overflow-wrap: anywhere; }
</style>

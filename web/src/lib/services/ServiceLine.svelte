<script lang="ts">
  // Service lines: one compact line per service with a dot, its name, one
  // short status and its controls. Tapping the name opens the details (turn
  // counts, limits, retention). A switch writes through the call that owns it
  // and shows the engine's answer on the same line. A service's profile shows
  // as text, because Settings > Models sets it. A service with no control shows none.
  import type { Row } from './model.ts'

  interface Props {
    rows: Row[]
    // status replaces the row's short status; a tab header passes its state line.
    status?: string
    // wrap lets a long status take more lines instead of cutting it.
    wrap?: boolean
    onWrite: (id: string, value: string | boolean) => Promise<void>
    onRun: () => Promise<void>
  }
  let { rows, status = '', wrap = false, onWrite, onRun }: Props = $props()

  let errors = $state<Record<string, string>>({})
  let open = $state<string[]>([])
  // Each line tracks its own write, so a write that ends on one line never
  // reopens the controls on another line that is still saving.
  let saving = $state<string[]>([])
  const isSaving = (id: string): boolean => saving.includes(id)
  const flip = (id: string): void => { open = open.includes(id) ? open.filter((o) => o !== id) : [...open, id] }

  const fail = (e: unknown): string => String((e as { message?: string })?.message || e)

  // save reports a refusal on the line it belongs to and then frees that line.
  async function save(row: Row, work: () => Promise<string>) {
    saving = [...saving, row.id]
    try {
      errors[row.id] = await work()
    } catch (e) {
      errors[row.id] = fail(e)
    } finally {
      saving = saving.filter((id) => id !== row.id)
    }
  }

  const write = (row: Row, id: string, value: string | boolean): Promise<void> =>
    save(row, async () => { await onWrite(id, value); return '' })

  const run = (row: Row): Promise<void> =>
    save(row, async () => { await onRun(); return '' })

  // The details a tap shows: what the short status leaves out.
  const details = (row: Row, shown: string): string[] => [row.detail !== shown ? row.detail : '', row.value].filter(Boolean)
</script>

<ul class="lines" aria-label="Services">
  {#each rows as row (row.id)}
    {@const shown = status || (row.state === 'on' ? row.detail : row.state)}
    {@const more = details(row, shown)}
    <li class="row">
      <div class="top">
        <button class="name" class:wrap aria-expanded={more.length ? open.includes(row.id) : undefined} disabled={!more.length} onclick={() => flip(row.id)}>
          <span class="dot {row.tone}" aria-hidden="true"></span>
          <span class="txt"><span class="nm">{row.name}</span> <span class="st">{shown}</span></span>
        </button>
        <div class="ctl">
          {#if row.profile}
            <span class="sel flat" title="Set in Settings > Settings > Models">{row.profile.value || 'auto'}</span>
          {/if}
          {#if row.id === 'update' && row.toggle}
            <button class="run" disabled={isSaving(row.id)} onclick={() => run(row)}>Run now</button>
          {/if}
          {#if row.toggle}
            {@const toggle = row.toggle}
            <button
              class="sw" class:on={toggle.on} role="switch" aria-checked={toggle.on}
              aria-label="{row.name} on" disabled={isSaving(row.id)}
              onclick={() => write(row, toggle.id, !toggle.on)}
            ><i aria-hidden="true"></i></button>
          {/if}
        </div>
      </div>
      {#if open.includes(row.id) && more.length}
        <p class="more">{more.join(' · ')}</p>
      {/if}
      {#if errors[row.id]}<p class="err" role="alert">{errors[row.id]}</p>{/if}
    </li>
  {/each}
</ul>

<style>
  .lines { list-style: none; margin: 0; padding: 0; }
  .row { padding: 2px 0; border-bottom: 1px solid var(--line-soft); }
  .row:last-child { border-bottom: 0; }
  .top { display: flex; align-items: center; gap: var(--s3); min-height: 36px; }
  .name { flex: 1; min-width: 0; display: flex; align-items: baseline; gap: var(--s2); text-align: left; font-size: var(--t-ui); }
  .name:disabled { cursor: default; }
  .txt { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .wrap .txt { white-space: normal; }
  .dot { flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--faint); align-self: center; }
  .dot.live { background: var(--gold); }
  .dot.on { background: var(--sage); }
  .dot.off { background: var(--line); }
  .dot.error { background: var(--coral); }
  .nm { font-weight: 600; color: var(--ink); }
  .st { color: var(--muted); font-size: var(--t-sm); }
  .ctl { flex: none; display: flex; align-items: center; gap: var(--s2); }
  .sw { display: inline-flex; align-items: center; }
  .sw i { width: 26px; height: 15px; border-radius: 9px; background: var(--field); border: 1px solid var(--line); position: relative; }
  .sw i::after { content: ''; position: absolute; left: 2px; top: 2px; width: 9px; height: 9px; border-radius: 50%; background: var(--muted); transition: left var(--motion-fast) var(--ease); }
  .sw.on i { background: rgba(151, 201, 149, 0.3); border-color: rgba(151, 201, 149, 0.5); }
  .sw.on i::after { left: 13px; background: var(--sage); }
  .sw:disabled { opacity: 0.6; }
  .sel { background: var(--field); color: var(--ink-2); border: 1px solid var(--line); border-radius: var(--r-xs); padding: 1px 5px; font: inherit; font-size: var(--t-sm); max-width: 7.5em; text-overflow: ellipsis; }
  .sel.flat { color: var(--muted); border-color: transparent; background: none; white-space: nowrap; }
  .run { color: var(--muted); border: 1px solid var(--line); border-radius: var(--r-xs); padding: 1px 6px; font-size: var(--t-sm); white-space: nowrap; }
  .run:disabled { opacity: 0.6; }
  .more { margin: 0 0 var(--s2) 15px; font-size: var(--t-sm); color: var(--muted); }
  .err { margin: 0 0 var(--s2) 15px; color: var(--coral); font-size: var(--t-sm); }
  /* A narrow screen is a phone: the switch and the profile get a 40 px row so a
     thumb can hit them, while their own size stays small. */
  @media (max-width: 860px) {
    .sw, .sel, .run { min-height: 40px; }
    .sw { padding: 0 2px; }
    .sel { padding: 0 8px; }
    .run { padding: 0 10px; }
    .name { min-height: 40px; align-items: center; }
  }
</style>

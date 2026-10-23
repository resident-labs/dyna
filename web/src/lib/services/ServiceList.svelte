<script lang="ts">
  // The services column: one line per service with its switch, its profile and
  // what it is doing. Tapping a name filters the activity; a switch or a
  // profile writes through the call that owns it and shows the engine's answer
  // on the same line.
  import { pickProfile } from './services.ts'
  import type { Row } from './model.ts'

  interface Props {
    rows: Row[]
    selected: string
    onSelect: (id: string) => void
    onWrite: (id: string, value: string | boolean) => Promise<void>
    onRun: () => Promise<void>
  }
  let { rows, selected, onSelect, onWrite, onRun }: Props = $props()

  let errors = $state<Record<string, string>>({})
  // Each line tracks its own write, so a write that ends on one line never
  // reopens the controls on another line that is still saving.
  let saving = $state<string[]>([])
  const isSaving = (id: string): boolean => saving.includes(id)

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

  // A profile that did not save goes back to the stored choice, so the menu
  // never shows a choice that was not saved.
  const pick = (event: Event & { currentTarget: HTMLSelectElement }, row: Row, profile: { id: string; value: string }): Promise<void> =>
    save(row, () => pickProfile(event.currentTarget, (chosen: string) => onWrite(profile.id, chosen), profile.value))

  const run = (row: Row): Promise<void> =>
    save(row, async () => { await onRun(); return '' })
</script>

<ul class="list" aria-label="Services">
  {#each rows as row (row.id)}
    <li class="row" class:on={selected === row.id}>
      <button class="name" aria-pressed={selected === row.id} onclick={() => onSelect(selected === row.id ? '' : row.id)}>
        <span class="dot {row.tone}" aria-hidden="true"></span><span class="nm">{row.name}</span><span class="st">{row.state}</span>
      </button>
      <div class="ctl">
        {#if row.toggle}
          {@const toggle = row.toggle}
          <button
            class="sw" class:on={toggle.on} role="switch" aria-checked={toggle.on}
            aria-label="{row.name} on" disabled={isSaving(row.id)}
            onclick={() => write(row, toggle.id, !toggle.on)}
          ><i aria-hidden="true"></i><span>{toggle.on ? 'on' : 'off'}</span></button>
        {:else if !row.profile}
          <span class="none">no switch</span>
        {/if}
        {#if row.profile}
          {@const profile = row.profile}
          {#if profile.options.length}
            <select
              class="sel" aria-label="{row.name} profile" value={profile.value} disabled={isSaving(row.id)}
              onchange={(e) => pick(e, row, profile)}
            >
              {#each profile.options as option}<option value={option}>{option || 'auto'}</option>{/each}
            </select>
          {:else}
            <span class="sel flat">{profile.value || 'auto'}</span>
          {/if}
        {/if}
        {#if row.id === 'update' && row.toggle}
          <button class="run" disabled={isSaving(row.id)} onclick={() => run(row)}>run now</button>
        {/if}
        <span class="detail">{row.detail}</span>
        {#if row.profileStatus}<span class="pstat">{row.profileStatus}</span>{/if}
      </div>
      {#if row.value}<span class="val">{row.value}</span>{/if}
      {#if errors[row.id]}<p class="err" role="alert">{errors[row.id]}</p>{/if}
    </li>
  {/each}
</ul>

<style>
  .list { list-style: none; margin: 0; padding: 0; }
  .row { padding: var(--s2) var(--s2) 9px; border-radius: var(--r-xs); }
  /* The rows that take pointer hover also carry its shared fast transition. */
  .row:not(.on) { transition: background-color var(--motion-fast) var(--ease); }
  .row.on { background: var(--select); }
  @media (hover: hover) {
    .row:not(.on):hover { background: var(--hover); }
  }
  /* The pressed surface is declared after the hover one, so a press wins on a
     device that also reports hover. */
  .row:not(.on):active { background: var(--select); }
  .name { display: flex; align-items: baseline; gap: var(--s2); width: 100%; text-align: left; font-size: var(--t-ui); }
  .dot { flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--faint); }
  .dot.live { background: var(--gold); }
  .dot.on { background: var(--sage); }
  .dot.off { background: var(--line); }
  .dot.error { background: var(--coral); }
  .nm { font-weight: 600; color: var(--ink); }
  .st { margin-left: auto; font-size: var(--t-sm); color: var(--muted); }
  .ctl { display: flex; align-items: center; gap: var(--s2); margin: var(--s2) 0 0 15px; font-size: var(--t-sm); color: var(--faint); flex-wrap: wrap; }
  .sw { display: inline-flex; align-items: center; gap: 5px; font-size: var(--t-sm); color: var(--muted); }
  .sw i { width: 26px; height: 15px; border-radius: 9px; background: var(--field); border: 1px solid var(--line); position: relative; }
  .sw i::after { content: ''; position: absolute; left: 2px; top: 2px; width: 9px; height: 9px; border-radius: 50%; background: var(--muted); transition: left var(--motion-fast) var(--ease); }
  .sw.on i { background: rgba(151, 201, 149, 0.3); border-color: rgba(151, 201, 149, 0.5); }
  .sw.on i::after { left: 13px; background: var(--sage); }
  .sw:disabled { opacity: 0.6; }
  .sel { background: var(--field); color: var(--ink-2); border: 1px solid var(--line); border-radius: var(--r-xs); padding: 1px 5px; font: inherit; font-size: var(--t-sm); max-width: 8em; }
  .sel.flat { color: var(--muted); }
  .run { color: var(--muted); border: 1px solid var(--line); border-radius: var(--r-xs); padding: 1px 6px; font-size: var(--t-sm); }
  .run:disabled { opacity: 0.6; }
  .none { color: var(--faint); font-size: var(--t-sm); }
  .detail { color: var(--muted); }
  .pstat { color: var(--muted); }
  .val { display: block; margin: var(--s1) 0 0 15px; font-size: var(--t-sm); color: var(--muted); font-variant-numeric: tabular-nums; }
  .err { margin: var(--s1) 0 0 15px; color: var(--coral); font-size: var(--t-sm); }
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

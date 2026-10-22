<script lang="ts">
  // One quiet row per service: status, what it is doing, profile, last and
  // next run and cost today. A row opens to its controls. A service with no
  // controls shows only its row; a field with no source reads "unknown".
  import { ago } from '../ui/format.js'
  import type { Card, Control } from './cards.ts'

  interface Props { cards: Card[]; now: number; onchange: (card: Card, control: Control, value: string | boolean) => Promise<string> }
  let { cards, now, onchange }: Props = $props()

  let open = $state('')
  let errors = $state<Record<string, string>>({})

  async function change(card: Card, control: Control, value: string | boolean) {
    errors[card.id] = await onchange(card, control, value)
  }
  const when = (iso: string) => (iso ? ago(iso) : 'unknown')
  function until(iso: string): string {
    const minutes = Math.round((new Date(iso).getTime() - now) / 60000)
    return minutes <= 0 ? 'due' : minutes < 60 ? `in ${minutes}m` : `in ${Math.round(minutes / 60)}h`
  }
  const next = (n: Card['next']) => (n.at ? until(n.at) : n.text ?? 'unknown')
</script>

<div class="cards">
  {#each cards as card (card.id)}
    <div class="svc" class:open={open === card.id}>
      <button class="head" class:can={card.controls.length > 0} aria-expanded={card.controls.length ? open === card.id : undefined} disabled={!card.controls.length} onclick={() => (open = open === card.id ? '' : card.id)}>
        <span class="dot {card.tone}" aria-hidden="true"></span>
        <span class="nm">{card.name}<span class="st {card.tone}">{card.status}</span></span>
        <span class="line">{card.line}</span>
        <span class="meta">
          <span><i>profile</i>{card.profile || '—'}</span>
          <span><i>last run</i>{when(card.last)}</span>
          <span><i>next run</i>{next(card.next)}</span>
          <span><i>cost today</i>{card.cost}</span>
        </span>
        <span class="chev" aria-hidden="true">{card.controls.length ? (open === card.id ? '⌄' : '›') : ''}</span>
      </button>
      {#if open === card.id}
        <div class="ctl">
          {#each card.controls as control (control.id)}
            <label>
              {#if control.kind === 'toggle'}
                <input type="checkbox" checked={control.value} onchange={(e) => change(card, control, e.currentTarget.checked)} /> {control.label}
              {:else if control.kind === 'select'}
                {control.label}
                <select value={control.value} onchange={(e) => change(card, control, e.currentTarget.value)}>
                  {#each control.options as option}<option value={option}>{option || 'auto'}</option>{/each}
                </select>
              {:else}
                {control.label}
                <input type="text" value={control.value} onchange={(e) => change(card, control, e.currentTarget.value)} />
              {/if}
            </label>
          {/each}
          {#if errors[card.id]}<span class="err" role="alert">{errors[card.id]}</span>{/if}
        </div>
      {/if}
    </div>
  {/each}
</div>

<style>
  .svc { border-radius: var(--r-xs); }
  .svc.open { background: var(--raise); }
  .head { display: grid; grid-template-columns: 8px minmax(0, 1fr) auto 14px; gap: 2px var(--s3); align-items: center; width: 100%; padding: 11px 10px; border-radius: var(--r-xs); text-align: left; transition: background-color var(--motion-fast) var(--ease); }
  .head:disabled { cursor: default; }
  @media (hover: hover) {
    .head.can:hover { background: var(--hover); }
  }
  .head.can:active { background: var(--select); }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--faint); }
  .dot.on { background: var(--sage); }
  .dot.running { background: var(--gold); }
  .dot.error { background: var(--coral); }
  .nm { font-weight: 600; font-size: var(--t-body); }
  .st { margin-left: var(--s3); font-size: var(--t-sm); font-weight: 400; color: var(--muted); }
  .st.on { color: var(--sage); }
  .st.running { color: var(--gold); }
  .st.error { color: var(--coral); }
  .line { grid-column: 2; grid-row: 2; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); font-size: var(--t-ui); }
  .meta { grid-column: 3; grid-row: 1 / 3; display: flex; gap: 18px; font-size: var(--t-sm); }
  .meta span { display: flex; flex-direction: column; width: 96px; }
  .meta i { font-style: normal; color: var(--faint); font-size: var(--t-cap); }
  .chev { grid-column: 4; grid-row: 1 / 3; color: var(--faint); }
  .ctl { display: flex; flex-wrap: wrap; gap: var(--s4); align-items: center; padding: 6px 12px 14px 30px; font-size: var(--t-ui); color: var(--muted); }
  .ctl select, .ctl input[type='text'] { background: var(--field); color: var(--ink); border: 1px solid var(--line); border-radius: var(--r-xs); padding: 4px 8px; font: inherit; }
  .err { color: var(--coral); }
  @media (max-width: 760px) {
    .head { grid-template-columns: 8px minmax(0, 1fr) auto 14px; row-gap: 4px; }
    .line { grid-column: 2 / 4; grid-row: 2; white-space: normal; }
    .meta { grid-column: 2 / 4; grid-row: 3; gap: 12px; flex-wrap: wrap; }
    .meta span { width: auto; min-width: 60px; }
    .chev { grid-row: 1; grid-column: 4; }
    .ctl { padding-left: var(--s3); }
  }
</style>

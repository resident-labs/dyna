<script lang="ts">
  let { label, value, detail = '', tone = 'muted' }: {
    label: string
    value: string
    detail?: string
    tone?: 'ok' | 'warn' | 'bad' | 'muted'
  } = $props()
</script>

<div class="tile">
  <span class="label">{label}</span>
  <strong class={tone}>{value}</strong>
  <span class="detail">{detail}</span>
</div>

<style>
  .tile { display: flex; flex-direction: column; gap: 3px; padding: var(--s2); border-right: 1px solid var(--hair); min-width: 0; }
  .label { color: var(--muted); font-size: var(--t-cap); text-transform: uppercase; letter-spacing: .08em; }
  strong { color: var(--ink); font: 600 var(--t-head) var(--mono); }
  strong.warn { color: var(--gold); } strong.bad { color: var(--coral); } strong.ok { color: var(--sage); }
  .detail { color: var(--faint); font-size: var(--t-cap); }
</style>

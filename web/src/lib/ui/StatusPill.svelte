<script lang="ts">
  let { label, value = '', detail = '', tone = 'muted' }: {
    label: string
    value?: string
    detail?: string
    tone?: 'ok' | 'warn' | 'bad' | 'muted'
  } = $props()
</script>

<span class="pill {tone}" title={detail || undefined}>{label}{#if value} {value}{/if}</span>

<style>
  .pill { display: inline-flex; align-items: center; border: 1px solid var(--hair); border-radius: 10px; padding: 1px 8px; color: var(--ink-2); font-size: var(--t-cap); }
  .ok { color: var(--sage); } .warn { color: var(--gold); } .bad { color: var(--coral); } .muted { color: var(--muted); }
</style>

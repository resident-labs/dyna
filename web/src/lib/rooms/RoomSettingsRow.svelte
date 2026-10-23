<script lang="ts">
  // One setting: its title and help on the left, its control on the right.
  // A wide row puts the control under the text. saved shows a short "Saved".
  import type { Snippet } from 'svelte'

  interface Props {
    title: string
    help?: string
    error?: string
    saved?: boolean
    wide?: boolean
    control?: Snippet | null
    children?: Snippet | null
  }
  let { title, help = '', error = '', saved = false, wide = false, control = null, children = null }: Props = $props()
</script>

<div class="row" class:wide>
  <div class="text">
    <div class="title"><span>{title}</span>{#if saved}<span class="ok">Saved</span>{/if}</div>
    {#if help}<p class="help">{help}</p>{/if}
    {#if error}<p class="err">{error}</p>{/if}
  </div>
  {#if control}<div class="ctl">{@render control()}</div>{/if}
  {#if children}<div class="body">{@render children()}</div>{/if}
</div>

<style>
  .row { display: flex; gap: var(--s5); align-items: center; padding: var(--s4) 18px; flex-wrap: wrap; }
  .text { flex: 1 1 260px; min-width: 0; }
  .title { display: flex; align-items: center; gap: var(--s2); font-size: var(--t-lead); font-weight: 600; }
  .ok { font-size: var(--t-sm); color: var(--sage); font-weight: 500; }
  .help { margin: var(--s1) 0 0; font-size: var(--t-ui); color: var(--muted); line-height: var(--leading); }
  .err { margin: 6px 0 0; font-size: var(--t-sm); color: var(--coral); }
  .ctl { flex: none; max-width: 100%; }
  .body { flex: 1 1 100%; min-width: 0; }
  .wide { gap: var(--s3); }
</style>

<script lang="ts">
  // The room's running computers: a picture tile for each. Tiles are
  // screenshots, never streams. Computers that are not running stay hidden
  // behind a quiet "Show stopped" toggle.
  import ComputerShot from './ComputerShot.svelte'
  import Card from '../ui/Card.svelte'
  import Row from '../ui/Row.svelte'
  import TabHeader from '../rooms/TabHeader.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import { clock } from './clock.svelte.ts'
  import Chip from '../ui/Chip.svelte'
  import type { ComputerInfo } from '../api/computer.ts'
  import { computerLabel, shotRatio, splitComputers, stateText, uptimeText } from './computers.ts'
  interface Props {
    list?: ComputerInfo[] | null
    error?: string
    onopen: (c: ComputerInfo) => void
  }
  let { list = [], error = '', onopen }: Props = $props()
  const split = $derived(splitComputers(list ?? []))
  let showStopped = $state(false)
</script>

<div class="page">
  <TabHeader title="Computers" blurb={split.tiles.length ? `${split.tiles.length} running` : ''} />
  <div class="body">
  {#if error}<p class="err">{error}</p>{/if}
  {#if !split.tiles.length}
    <EmptyState title="No computers running">An agent starts one when it needs a browser or desktop app.</EmptyState>
  {/if}
  {#if split.tiles.length}
    <div class="grid">
      {#each split.tiles as c (c.session)}
        <button class="g" onclick={() => onopen(c)} aria-label={`${computerLabel(c)}: ${c.state}`}>
          <div class="shot" style:aspect-ratio={shotRatio(c)}>
            <ComputerShot session={c.session} shotAt={c.shotAt} phase={c.state} refresh={15000} alt="" />
          </div>
          <div class="cap">
            <span class="n">{computerLabel(c)}</span>
            <span class="meta">
              {#if c.state === 'running'}<Chip tone="success">Running</Chip>{:else}<Chip tone="live">Starting…</Chip>{/if}
              <span class="s">{uptimeText(c.startedAt, clock.now)}</span>
            </span>
          </div>
        </button>
      {/each}
    </div>
  {/if}
  {#if split.rows.length}
    <button class="toggle" aria-expanded={showStopped} onclick={() => (showStopped = !showStopped)}>{showStopped ? 'Hide stopped' : `Show stopped (${split.rows.length})`}</button>
  {/if}
  {#if showStopped && split.rows.length}
    <Card title="Stopped" count={split.rows.length}>
      {#each split.rows as c (c.session)}
        <Row dense text={computerLabel(c)} onclick={() => onopen(c)}>
          {#snippet trail()}<span class="s status" title={stateText(c)}>{stateText(c)}</span>{/snippet}
        </Row>
      {/each}
    </Card>
  {/if}
  </div>
</div>

<style>
  .page { overflow-y: auto; flex: 1; min-height: 0; }
  .body { padding: 0 var(--s5) var(--s5); display: grid; gap: var(--s3); align-content: start; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: var(--s3); }
  .g { border-radius: var(--r-sm); box-shadow: inset 0 0 0 1px var(--hair); overflow: hidden; background: var(--side); padding: 0; text-align: left; display: flex; flex-direction: column; width: 100%; cursor: pointer; transition: box-shadow var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .g:hover { box-shadow: inset 0 0 0 1px var(--iris-edge); }
  }
  .shot { position: relative; background: #0f0f11; }
  .cap { padding: var(--s2) var(--s3); display: grid; gap: var(--s1); min-width: 0; }
  .cap .n { font-weight: 600; font-size: var(--t-sm); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meta { display: flex; align-items: center; gap: var(--s2); min-width: 0; }
  .s { font-size: var(--t-cap); color: var(--faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .status { margin-left: auto; }
  .err { color: var(--coral); margin: 0; }
  .toggle { justify-self: center; font-size: var(--t-cap); color: var(--faint); transition: color var(--motion-fast) var(--ease); text-decoration: underline; text-underline-offset: 3px; }
  @media (hover: hover) { .toggle:hover { color: var(--muted); } }
  @media (max-width: 760px) { .status { max-width: 48%; flex-shrink: 1; } .body { padding: 0 var(--s4) var(--s4); } .grid { grid-template-columns: 1fr 1fr; gap: var(--s2); } }
</style>

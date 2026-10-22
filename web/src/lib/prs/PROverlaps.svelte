<script lang="ts">
  // The conflict radar for one change: for each other open change that
  // touches the same files, the files that conflict first, then the files
  // both change. overlaps is the change's saved Overlap list.
  import type { Overlap } from '../api/change.ts'
  let { overlaps = [] }: { overlaps?: Overlap[] } = $props()
  const shared = (o: Overlap) => (o.files ?? []).filter((f) => !(o.conflicts ?? []).includes(f))
</script>

{#each overlaps ?? [] as o (o.changeId)}
  <section class="overlap">
    <h4>Overlaps #{o.number}</h4>
    <ul>
      {#each o.conflicts ?? [] as f (f)}<li class="conflict"><code>{f}</code> <span>conflicts</span></li>{/each}
      {#each shared(o) as f (f)}<li><code>{f}</code></li>{/each}
    </ul>
  </section>
{/each}

<style>
  .overlap { margin: var(--s3) 0; }
  h4 { margin: 0 0 var(--s1); font-size: 13px; font-weight: 400; color: var(--muted); }
  ul { margin: 0; padding: 0; list-style: none; }
  li { font-size: var(--t-sm); color: var(--muted); overflow-wrap: anywhere; }
  li code { font: var(--t-cap) var(--mono); }
  .conflict, .conflict span { color: var(--gold); }
</style>

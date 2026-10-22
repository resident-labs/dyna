<script lang="ts">
  // Who may use each profile: one row per profile and one checkbox per use
  // (internal/profile Uses). The last use of a profile stays on, and a
  // disabled profile is changed by enabling it first.
  //
  // Props: profiles (ProfileView rows, each with every allowed use); busy
  // (names whose row is saving and locked); onchange(profile, uses) saves one change.
  // A box keeps the saved state until the reloaded list shows the change.
  import { USES, toggleUse } from './profiles/state.ts'
  import type { ProfileView } from '../api/profile.ts'
  let { profiles = [], busy = [], onchange = () => {} }: { profiles?: ProfileView[]; busy?: string[]; onchange?: (profile: ProfileView, uses: string[]) => void } = $props()
</script>

<table class="uses">
  <thead><tr><th scope="col"><span class="sr">Profile</span></th>{#each USES as [, label]}<th scope="col">{label}</th>{/each}</tr></thead>
  <tbody>
    {#each profiles as p (p.name)}
      <tr class:off={p.disabled}>
        <th scope="row">{p.name}</th>
        {#each USES as [u]}
          {@const uses = p.uses ?? []}
          {@const on = uses.includes(u)}
          <td><input type="checkbox" aria-label="{p.name}: {u}" checked={on} disabled={p.disabled || busy.includes(p.name) || (on && uses.length === 1)} onchange={(e) => { const want = e.currentTarget.checked; e.currentTarget.checked = on; onchange(p, toggleUse(uses, u, want)) }} /></td>
        {/each}
      </tr>
    {/each}
  </tbody>
</table>

<style>
  .uses { border-collapse: collapse; font-size: var(--t-ui); }
  th, td { padding: 5px var(--s3); text-align: center; }
  thead th { font-size: var(--t-cap); font-weight: 600; color: var(--muted); }
  tbody th { text-align: left; font-weight: 500; color: var(--ink-2); padding-left: 0; }
  tbody tr + tr { box-shadow: 0 -1px 0 var(--hair); }
  tr.off { opacity: .55; }
  input { accent-color: var(--iris); }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
</style>

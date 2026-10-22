<script lang="ts">
  import { harnessIcons } from './icons.ts'
  let { id }: { id: string } = $props()
</script>

<span class="logo" aria-hidden="true"><img src={harnessIcons[id]} alt="" width="23" height="23" draggable="false" /></span>

<style>
  .logo { display: grid; place-items: center; width: 35px; height: 35px; flex: none; border: 1px solid var(--line); background: var(--raise); border-radius: 9px; }
  img { display: block; object-fit: contain; }
  @media (max-width: 540px) { .logo { width: 31px; height: 31px; } img { width: 22px; height: 22px; } }
</style>

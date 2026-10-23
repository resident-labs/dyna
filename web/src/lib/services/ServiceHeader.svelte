<script lang="ts">
  // The one line at the top of a service page: the service name, a short
  // status, a plain note when its profile covers all rooms, and the profile
  // menu. A room has one repository, so a room-scoped service needs no note.
  import { api } from '../../api.js'
  import type { ServiceView } from '../api/service.ts'
  import { serviceStatus, pinNotEnabled, pickProfile, errorText } from './services.ts'

  // role is "merge-steward" or "reviewer". The header loads its ListServices row,
  // which names the profiles the service can run; pages pass only roomId and
  // role. The service prop exists so server-render tests can give data.
  // onchange runs after a saved choice, so the page can reload its status.
  interface Props { roomId: string; role: string; service?: ServiceView | null; onchange?: () => unknown }
  let { roomId, role, service = null, onchange = () => {} }: Props = $props()
  let profiles = $derived(service?.profiles ?? [])
  let busy = $state(false)
  let error = $state('')
  let status = $derived(serviceStatus(service))
  async function load() {
    const room = roomId
    const rows: ServiceView[] = await api.listServices(room)
    const row = rows.find((s) => s.role === role) ?? null
    if (room === roomId) service = row
  }
  const fail = (e: unknown) => (error = errorText(e))
  $effect(() => {
    load().catch(fail)
  })
  async function pick(e: Event & { currentTarget: HTMLSelectElement }) {
    const select = e.currentTarget
    busy = true
    error = await pickProfile(select, (profile) => api.setServiceProfile(roomId, role, profile), service?.pinned ?? '')
    if (!error) await Promise.all([load(), onchange()]).catch(fail)
    busy = false
  }
</script>

{#if service}
  <section class="bar" aria-label={service.name}>
    <span class="name">{service.name}</span>
    <span class="status {status.tone}">{status.text}</span>
    {#if service.scope === 'global'}<span class="scope">Applies to all rooms</span>{/if}
    <select aria-label="{service.name} profile" value={service.pinned} onchange={pick} disabled={busy}>
      <option value="">Auto</option>
      {#if pinNotEnabled(service.pinned, profiles)}<option value={service.pinned}>{service.pinned} (not enabled)</option>{/if}
      {#each profiles as p}<option value={p}>{p}</option>{/each}
    </select>
  </section>
{/if}
{#if error}<p class="err">{error}</p>{/if}

<style>
  .bar { display: flex; align-items: center; gap: 8px; padding: 6px 14px; min-width: 0; color: var(--faint); }
  .name { font-weight: 500; font-size: 12.5px; flex: none; color: var(--muted); }
  .status { flex: 1; min-width: 0; font-size: 12px; color: var(--faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .status.coral { color: var(--coral); }
  .scope { flex: none; font-size: 12px; color: var(--faint); }
  select { flex: none; background: transparent; border: 0; border-radius: 4px; color: var(--muted); max-width: 150px; font: inherit; font-size: 12px; padding: 2px 4px; }
  select:disabled { opacity: 0.5; }
  .err { margin: 0; padding: 4px 14px; color: var(--coral); font-size: 12.5px; }
  @media (max-width: 760px) { select { min-height: 44px; } }
</style>

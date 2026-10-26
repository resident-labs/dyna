<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { api } from '../../../api.ts'
  import TabHeader from '../../rooms/TabHeader.svelte'
  import Loading from '../Loading.svelte'
  import Switch from '../../ui/Switch.svelte'
  import EmptyState from '../../ui/EmptyState.svelte'
  import InlineConfirm from '../../ui/InlineConfirm.svelte'
  import SkillForm from './SkillForm.svelte'
  import { failText } from '../harnesses/state.ts'
  import type { HarnessConfig, ManagedSkill } from '../../api/harness.ts'

  // Dyna's skill catalog. Every session reads each enabled skill through its
  // instructions, so a skill has one global switch and no per-harness choice.
  let catalog = $state<HarnessConfig | null>(null)
  let loading = $state(true)
  let busy = $state(false)
  let error = $state('')
  let notice = $state('')
  let adding = $state(false) // the add form is open
  let removing = $state('') // the skill waiting for an inline confirm

  let stopped = false
  const skills = $derived(catalog?.skills ?? [])

  async function load() {
    if (stopped) return
    loading = true
    error = ''
    try {
      const next = await api.harnessConfig()
      if (!stopped) catalog = next
    } catch (e) {
      if (!stopped) error = failText(e, 'Could not load skills.')
    } finally {
      if (!stopped) loading = false
    }
  }

  onMount(load)
  onDestroy(() => { stopped = true })

  function added(next: HarnessConfig) {
    if (stopped) return
    catalog = next
    adding = false
    error = ''
    notice = 'Skill added. Every session can read it from its next turn.'
  }

  async function setEnabled(entry: ManagedSkill, enabled: boolean) {
    if (busy || stopped) return
    busy = true
    error = ''
    notice = ''
    try {
      // An empty harness ID means the switch covers every session.
      const next = await api.setHarnessConfigEnabled('skill', entry.name, '', enabled)
      if (!stopped) {
        catalog = next
        notice = enabled ? 'Skill on. New turns can read it.' : 'Skill off. New turns no longer read it.'
      }
    } catch (e) {
      if (!stopped) error = failText(e, 'Could not change this skill. Try again.')
    } finally {
      if (!stopped) busy = false
    }
  }

  async function removeSkill() {
    if (busy || stopped || !removing) return
    const name = removing
    busy = true
    error = ''
    notice = ''
    try {
      const next = await api.removeHarnessConfig('skill', name)
      if (!stopped) {
        catalog = next
        removing = ''
        notice = 'Skill removed.'
      }
    } catch (e) {
      if (!stopped) error = failText(e, 'Could not remove this skill.')
    } finally {
      if (!stopped) busy = false
    }
  }
</script>

<section class="skills" aria-busy={loading || busy}>
  <TabHeader flush title="Skills" blurb="Dyna owns the catalog. Every session sees each enabled skill’s name, description and SKILL.md path in its instructions.">
    {#snippet action()}<button class="btn" onclick={() => (adding = !adding)}>{adding ? 'Close' : 'Add skill'}</button>{/snippet}
  </TabHeader>

  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if notice}<p class="notice" role="status">{notice}</p>{/if}

  {#if adding}
    <SkillForm oncancel={() => (adding = false)} onsave={added} />
  {/if}

  {#if loading && !catalog}
    <Loading>Loading skills…</Loading>
  {:else if !skills.length}
    <EmptyState title="No skills yet">Add a skill with a name and the absolute directory that holds its SKILL.md. Every session can then read it.</EmptyState>
  {:else}
    <div class="thead" aria-hidden="true"><span>Skill</span><span class="on" title="On for every session">On</span><span></span></div>
    {#each skills as entry (entry.name)}
      <div class="srow">
        <div class="what">
          <div class="name">{entry.name}</div>
          <div class="mono src" title={entry.source}>{entry.source}</div>
        </div>
        <Switch on={entry.enabled} label={`${entry.name} enabled`} disabled={busy} onchange={(v: boolean) => setEnabled(entry, v)} />
        <div class="act">
          {#if removing !== entry.name}
            <button class="remove" aria-label={`Remove ${entry.name} skill`} disabled={busy} onclick={() => (removing = entry.name)}>Remove</button>
          {/if}
        </div>
      </div>
      {#if removing === entry.name}
        <div class="ask"><InlineConfirm ask="Remove skill?" {busy} onconfirm={removeSkill} oncancel={() => (removing = '')} /></div>
      {/if}
    {/each}
  {/if}
</section>

<style>
  .skills { container-type: inline-size; }
  .error { color: var(--coral); font-size: var(--t-ui); margin: var(--s3) 0 0; }
  .notice { color: var(--muted); font-size: var(--t-sm); margin: var(--s3) 0 0; }
  /* The name and source take the room the switch and the Remove button leave,
     so the source path keeps a useful width in a narrow settings pane. */
  .thead, .srow { display: grid; grid-template-columns: minmax(0, 1fr) 56px 64px; align-items: center; gap: var(--s3); padding: 11px 0; border-bottom: 1px solid var(--line-soft); }
  .thead { font-size: var(--t-cap); color: var(--muted); }
  .thead .on { text-align: center; }
  .srow { padding: 14px 0; }
  .srow > :global(.switch) { justify-self: center; }
  .name { font-size: var(--t-body); font-weight: 500; overflow-wrap: anywhere; }
  .src { color: var(--muted); font-size: var(--t-cap); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .act { justify-self: end; }
  .remove { color: var(--muted); font-size: var(--t-cap); text-decoration: underline; }
  .ask { padding: 0 0 var(--s3); }
  /* A phone pane is narrower than the three columns need: the Remove action
     drops to its own line. */
  @container (max-width: 400px) {
    .thead { display: none; }
    .srow { grid-template-columns: minmax(0, 1fr) 56px; row-gap: var(--s2); }
    .act { grid-column: 1 / -1; justify-self: start; }
  }
</style>

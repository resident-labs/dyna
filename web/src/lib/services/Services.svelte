<script lang="ts">
  // The Services page: everything Dyna runs by itself, in the sections the
  // engine's service registry gives, then the hooks the owner added. One page,
  // no tabs. Each row says what the service does and, as muted text, when it
  // ran and runs next; a colored pill appears only when it needs attention. A
  // click opens the service's settings and recent runs in place.
  import { api } from '../../api.ts'
  import { errorText } from '../errorText.ts'
  import TabHeader from '../rooms/TabHeader.svelte'
  import Section from '../ui/Section.svelte'
  import Card from '../ui/Card.svelte'
  import SettingRow from '../ui/SettingRow.svelte'
  import StatusPill from '../ui/StatusPill.svelte'
  import Switch from '../ui/Switch.svelte'
  import Skeleton from '../ui/Skeleton.svelte'
  import ServiceDetail from './ServiceDetail.svelte'
  import HooksSection from './HooksSection.svelte'
  import { factsOf, liveOf, pillOf, sections, setupHint } from './model.ts'
  import { switchWrite } from './control.ts'
  import { readRegistry } from './read.ts'
  import type { ChangeRow } from './activity.ts'
  import type { Member } from '../api/room.ts'
  import type { RegistryService } from '../api/service.ts'
  import type { SettingItem } from '../api/settings.ts'
  import type { HooksFocus } from '../app/settingsPanel.svelte.ts'

  let { roomId, repo = '', members = [], changes = [], hooks = null, onopen, onpr }: {
    roomId: string
    repo?: string
    members?: Member[]
    changes?: ChangeRow[]
    hooks?: HooksFocus | null
    onopen?: (m: Member) => void
    onpr?: (pr: number) => void
  } = $props()

  const host = $derived({ room: roomId, repo })
  let registry = $state<RegistryService[] | null>(null)
  let failed = $state('')
  // Settings need the admin scope; without it the details show no keys.
  let settings = $state<SettingItem[] | null>(null)
  let now = $state(Date.now())

  // The page reads the registry again every minute, so a change made
  // elsewhere shows without a reload. A failed read keeps the rows it had.
  async function reload(): Promise<void> {
    await api.serviceRegistry(roomId, repo).then((v) => { registry = readRegistry(v) ?? []; failed = '' }, (e: unknown) => { failed = errorText(e) })
    now = Date.now()
  }
  $effect(() => {
    reload()
    api.settings().then((v) => { settings = v ?? [] }, () => { settings = null })
    const t = setInterval(reload, 60 * 1000)
    return () => clearInterval(t)
  })

  const groups = $derived(sections(registry ?? []))
  let open = $state('') // the service whose detail shows
  let notSetUp = $state(false) // the Not set up group is open
  let saving = $state<string[]>([])
  let errors = $state<Record<string, string>>({})

  // A switch writes through its call and then reads the registry again, so the
  // row shows the engine's state and not what the click hoped for.
  async function flip(s: RegistryService, write: (on: boolean) => Promise<unknown>, on: boolean): Promise<void> {
    saving = [...saving, s.id]
    errors = { ...errors, [s.id]: '' }
    await write(on).catch((e: unknown) => { errors = { ...errors, [s.id]: errorText(e) } })
    await reload()
    saving = saving.filter((id) => id !== s.id)
  }

  async function saveSetting(key: string, value: string): Promise<void> {
    const item = await api.setSetting(key, value)
    settings = settings?.map((x) => (x.key === key ? item : x)) ?? null
    await reload()
  }

  const member = (session: string): Member | undefined => (session ? members.find((m) => m.id === session || m.address === session) : undefined)
  const openSession = (session: string): void => {
    const m = member(session)
    if (m && onopen) onopen(m)
  }
  const toggle = (id: string): void => { open = open === id ? '' : id }
</script>

{#snippet serviceRow(s: RegistryService)}
  {@const pill = pillOf(s)}
  {@const live = liveOf(s)}
  {@const write = s.switch ? switchWrite(api, host, s.id) : null}
  {#snippet detail()}
    <ServiceDetail service={s} {host} {settings} {changes} canOpen={(session: string) => !!member(session) && !!onopen} onopen={openSession} {onpr} onchanged={reload} onsetting={saveSetting} />
  {/snippet}
  <SettingRow inline title={s.label} description={s.description} expanded={open === s.id} ontoggle={() => toggle(s.id)} children={open === s.id ? detail : undefined}>
    {#snippet status()}
      {#if pill}<StatusPill label={pill.label} tone={pill.tone} />{/if}
      {#if live}<StatusPill label={live} tone="live" variant="dot" />{/if}
      <span>{factsOf(s, now).join(' · ')}</span>
      {#if errors[s.id]}<span class="err" role="alert">{errors[s.id]}</span>{/if}
    {/snippet}
    {#snippet control()}
      {#if s.switch}
        <Switch on={s.switch.on} label="{s.label} runs" disabled={!write || saving.includes(s.id)} onchange={(on: boolean) => write && flip(s, write, on)} />
      {:else if s.state === 'not-set-up' && (s.settingsKeys.length || setupHint[s.id])}
        <button class="btn quiet" aria-expanded={open === s.id} onclick={() => toggle(s.id)}>Set up</button>
      {/if}
    {/snippet}
  </SettingRow>
{/snippet}

<section class="services" aria-label="Services">
  <TabHeader title="Services" blurb="What Dyna runs by itself, and the commands you added." />
  <div class="page">
    {#if failed}<p class="err" role="alert">The service list could not be read: {failed}</p>{/if}
    {#if registry === null && !failed}
      <Card flush><Skeleton label="Loading services" rows={4} /></Card>
    {/if}
    {#each groups as g (g.id)}
      <Section title={g.title} description={g.description}>
        <Card flush divided>
          {#if g.id === 'not-set-up'}
            <SettingRow title={g.services.map((s) => s.label).join(', ')} description="{g.services.length} {g.services.length === 1 ? 'service has' : 'services have'} nothing to do yet." expanded={notSetUp} ontoggle={() => (notSetUp = !notSetUp)} />
            {#if notSetUp}{#each g.services as s (s.id)}{@render serviceRow(s)}{/each}{/if}
          {:else}
            {#each g.services as s (s.id)}{@render serviceRow(s)}{/each}
          {/if}
        </Card>
      </Section>
    {/each}
    <HooksSection {repo} focus={hooks} />
  </div>
</section>

<style>
  .services { flex: 1; min-height: 0; overflow: auto; container-type: inline-size; }
  .page { max-width: 760px; padding: 0 var(--s5) var(--s6); }
  .err { margin: 0 0 var(--s3); font-size: var(--t-sm); color: var(--tone-danger-fg); }
  @container (max-width: 640px) {
    .page { padding: 0 var(--s3) var(--s5); }
  }
</style>

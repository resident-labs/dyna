<script lang="ts">
  // Every `dyna settings` key with a control that fits its kind. A value
  // saves on change (toggles, choices) or on Enter and blur (text).
  import { errorText } from '../errorText.ts'
  import { onMount } from 'svelte'
  import { api } from '../../api.js'
  import { settingDisplay } from './display.ts'
  import { fade } from 'svelte/transition'
  import Switch from '../ui/Switch.svelte'
  import Segmented from '../ui/Segmented.svelte'
  import LookRows from '../rooms/LookRows.svelte'
  import Feed from '../rooms/Feed.svelte'
  import { sampleRoom, samplePosts } from '../rooms/feedSample.js'
  import { look } from '../rooms/look.svelte.js'
  import { visibleTo, scopeOf } from '../rooms/look.ts'
  import { showReasoning, saveShowReasoning, showCompaction, saveShowCompaction, channelList, saveChannelList, CHANNEL_LISTS } from '../ui/prefs.ts'
  import { FAST, motion } from '../ui/motion.js'
  import TabHeader from '../rooms/TabHeader.svelte'
  import type { SettingItem } from '../api/settings.ts'

  let items = $state<SettingItem[]>([])
  let error = $state('')
  let errors = $state<Record<string, string>>({}) // key -> validation error
  let saved = $state('') // key that just saved
  let drafts = $state<Record<string, string | undefined>>({}) // key -> text being typed

  const titles: Record<string, string> = {
    'voice.ducking': 'Lower Voice while I talk',
    'browser.path': 'Chromium path',
    'browser.extraFlags': 'Extra flags',
    'browser.useFlagsFile': 'Use my chromium-flags.conf',
    'browser.profileMode': 'Profile mode',
    'browser.ownerProfile': 'My Chromium profile',
    'browser.saveBack': 'Save cookies back when a computer stops',
    'goals.keeperProfile': 'Keeper model',
    'goals.keeperTimeout': 'Keeper pass deadline',
    'content.maxCardBytes': 'HTML/JavaScript card size',
    'content.maxFileBytes': 'File attachment size',
    'updater.autoCheck': 'Check for updates',
    'worktrees.retention': 'Keep workflow worktrees',
    'root.profile': 'Default root profile',
    'subagent.maxDepth': 'Session nesting depth',
    'steering.maxToolWait': 'Interrupt a busy tool after',
  }
  const groups = [
    { title: 'Voice', keys: ['voice.ducking'] },
    { title: 'Browser', keys: ['browser.path', 'browser.extraFlags', 'browser.useFlagsFile', 'browser.profileMode', 'browser.ownerProfile', 'browser.saveBack'] },
    { title: 'Cards and files', keys: ['content.maxCardBytes', 'content.maxFileBytes'] },
    { title: 'Sessions', keys: ['root.profile', 'subagent.maxDepth', 'steering.maxToolWait'] },
    { title: 'Goals', keys: ['goals.keeperProfile', 'goals.keeperTimeout'] },
    { title: 'Workflows', keys: ['worktrees.retention'] },
    { title: 'App', keys: ['updater.autoCheck'] },
  ]
  // The preview filters like a channel does (App.svelte), so Hidden hides.
  let previewPosts = $derived(samplePosts.filter(visibleTo(look, scopeOf(sampleRoom.id, 'general'))))
  let byKey = $derived(Object.fromEntries(items.map((it) => [it.key, it])))

  function visible(key: string) {
    if (key === 'browser.ownerProfile') return byKey['browser.profileMode']?.value === 'copy-mine'
    if (key === 'browser.saveBack') return byKey['browser.profileMode']?.value === 'shared'
    return true
  }

  function missingLabel(it: SettingItem) {
    if (it.key === 'browser.profileMode' && it.value === 'copy-mine') return 'Copy my Chromium profile (unavailable)'
    return String(it.value) + ' (not found)'
  }

  async function load() {
    try {
      items = (await api.settings()) ?? []
      error = ''
    } catch (e) {
      error = String(e)
    }
  }

  async function set(key: string, value: unknown) {
    errors = { ...errors, [key]: '' }
    try {
      const it = await api.setSetting(key, String(value))
      // Both content controls show a derived total, so refresh them together.
      items = key.startsWith('content.')
        ? ((await api.settings()) ?? [])
        : items.map((x) => (x.key === key ? it : x))
      drafts = { ...drafts, [key]: undefined }
      saved = key
      setTimeout(() => { if (saved === key) saved = '' }, 1500)
    } catch (e) {
      errors = { ...errors, [key]: errorText(e) }
    }
  }

  function textKey(e: KeyboardEvent & { currentTarget: HTMLInputElement }, it: SettingItem) {
    if (e.key === 'Enter') e.currentTarget.blur()
    if (e.key === 'Escape') {
      e.stopPropagation()
      drafts = { ...drafts, [it.key]: undefined }
      e.currentTarget.blur()
    }
  }

  function commit(it: SettingItem) {
    const v = drafts[it.key]
    if (v !== undefined && (String(v) !== String(settingDisplay(it)) || errors[it.key])) set(it.key, v)
  }

  const sourceText: Record<string, string> = { default: 'default', file: 'set', env: 'from environment', invalid: 'invalid file' }

  onMount(load)
</script>

<section>
  <TabHeader flush title="Settings" blurb="Every setting below uses the same key as the dyna settings command." />
  <h3 class="section-label">Display</h3>
  <div class="group">
    <div class="row">
      <div class="text">
        <div class="title">Show reasoning</div>
        <p class="help">Show reasoning in session transcripts. Saves in this browser and applies at once.</p>
      </div>
      <Switch on={$showReasoning} label="Show reasoning" onchange={saveShowReasoning} />
    </div>
    <div class="row">
      <div class="text">
        <div class="title">Show compaction dividers</div>
        <p class="help">Show a divider where the harness compacted its context in session transcripts. Saves in this browser and applies at once.</p>
      </div>
      <Switch on={$showCompaction} label="Show compaction dividers" onchange={saveShowCompaction} />
    </div>
    <div class="row">
      <div class="text">
        <div class="title">Channels in the sidebar</div>
        <p class="help">Folded keeps #general and folds the other channels into one row. Messages only shows one Messages row that opens #general. Saves in this browser.</p>
      </div>
      <Segmented label="Channels in the sidebar" options={CHANNEL_LISTS} value={$channelList} onchange={saveChannelList} />
    </div>
  </div>
  <h3 class="section-label">Feed</h3>
  <p class="help browser-note">The default for every channel. A channel’s top-bar menu can change it for that channel.</p>
  <div class="group"><LookRows /></div>
  <div class="preview" aria-label="Feed preview" inert>
    <Feed room={sampleRoom} posts={previewPosts} intro={false} onearlier={undefined} onnewer={undefined} onpresent={undefined} onaround={undefined} onacceptcall={undefined} ondeclinecall={undefined} onaddress={() => {}} onreveal={() => {}} onreply={() => {}} onforward={() => {}} onpin={() => {}} onopenfile={() => {}} ondetail={() => {}} onquestion={() => {}} ongoal={() => {}} />
  </div>
  {#if error}<p class="err">{error}</p>{/if}
  {#each groups as g}
    <h3 class="section-label">{g.title}</h3>
    {#if g.title === 'Browser'}<p class="help browser-note">Changes apply at the next Chromium start.</p>{/if}
    <div class="group">
      {#each g.keys as key}
        {@const it = byKey[key]}
        {#if it && visible(key)}
          <div class="row" class:browser={key.startsWith('browser.')}>
            <div class="text">
              <div class="title">
                <span>{titles[key] ?? key}</span>
                <span class="src" class:set={it.source !== 'default'}>{sourceText[it.source] ?? it.source}</span>
                {#if saved === key}<span class="ok" transition:fade={{ duration: motion(FAST) }}>Saved</span>{/if}
              </div>
              <p class="help">{it.help}</p>
              <p class="key mono">{key}</p>
              {#if errors[key]}<p class="err small">{errors[key]}</p>{/if}
            </div>
            <div class="ctl">
              {#if it.kind === 'bool'}
                <Switch on={!!it.value} label={titles[key]} disabled={it.source === 'env'} onchange={(v: boolean) => set(key, v)} />
              {:else if it.kind === 'select'}
                <select class="field profile" aria-label={titles[key]} value={it.value} onchange={(e) => set(key, e.currentTarget.value)}>{#if !it.options?.some((o) => o === it.value)}<option value={it.value} disabled>{missingLabel(it)}</option>{/if}{#each it.options ?? [] as value, i}<option {value}>{it.optionLabels?.[i] ?? value}</option>{/each}</select>
              {:else if it.kind === 'enum'}
                <Segmented options={(it.options ?? []).map((o) => [o, o])} value={it.value} label={titles[key]} onchange={(o: string) => set(key, o)} />
              {:else}
                <input
                  class="field mono"
                  class:bad={errors[key]}
                  value={drafts[key] ?? settingDisplay(it)}
                  inputmode={it.kind === 'int' ? 'numeric' : 'text'}
                  oninput={(e) => (drafts = { ...drafts, [key]: e.currentTarget.value })}
                  onkeydown={(e) => textKey(e, it)}
                  onblur={() => commit(it)}
                  aria-label={titles[key]}
                />
              {/if}
            </div>
          </div>
        {/if}
      {/each}
    </div>
  {/each}
</section>

<style>
  .group { background: var(--side); border-radius: 10px; border: 1px solid var(--line-soft); }
  .row { display: flex; gap: var(--s5); align-items: center; padding: var(--s4) 18px; }
  .row + .row { border-top: 1px solid var(--line-soft); }
  .text { flex: 1; min-width: 0; }
  .title { display: flex; align-items: center; gap: var(--s2); font-size: var(--t-lead); font-weight: 600; }
  .src { font-size: var(--t-cap); font-weight: 600; color: var(--faint); background: var(--raise); border-radius: 4px; padding: 1px 6px; }
  .src.set { color: var(--iris); background: var(--iris-wash); }
  .ok { font-size: var(--t-sm); color: var(--sage); font-weight: 500; }
  .help { margin: var(--s1) 0 0; font-size: var(--t-ui); color: var(--muted); line-height: 1.45; }
  .key { margin: var(--s1) 0 0; font-size: var(--t-cap); color: var(--faint); }
  .err { color: var(--coral); }
  .small { font-size: var(--t-sm); margin: 6px 0 0; }
  .ctl { flex: none; }
  /* The feed preview follows the Feed choices above as they change. */
  .preview { display: flex; flex-direction: column; height: 420px; margin-top: var(--s3); border: 1px solid var(--line-soft); border-radius: 10px; overflow: hidden; background: var(--main); }
  .field { width: 120px; background: var(--rail); border: 1px solid transparent; border-radius: 6px; color: var(--ink); padding: 7px 10px; font-size: var(--t-ui); outline: none; text-align: right; }
  .field.profile { width: 280px; max-width: 100%; text-align: left; }
  .row:has(select), .row:has(:global([role='radiogroup'])) { flex-wrap: wrap; }
  .row:has(select) .text, .row:has(:global([role='radiogroup'])) .text { flex-basis: 260px; }
  .ctl:has(select) { max-width: 100%; }
  .browser-note { margin: 0 0 10px; }
  .browser .field { width: 280px; max-width: 100%; text-align: left; }
  .browser { flex-wrap: wrap; }
  .browser .text { flex-basis: 260px; }
  .browser .ctl { max-width: 100%; }
  .field:focus { border-color: var(--iris-edge); }
  .field.bad { border-color: color-mix(in srgb, var(--coral) 55%, transparent); }
</style>

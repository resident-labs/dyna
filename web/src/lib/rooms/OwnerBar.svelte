<script lang="ts">
  import type { RouteNotice } from '../settings/link.ts'
  import Avatar from '../ui/Avatar.svelte'
  import { ownerIdentity } from '../ui/avatar.ts'
  import PushBell from '../settings/PushBell.svelte'
  import { dialog } from '../ui/dialog.ts'
  import { errorText } from '../errorText.ts'
  import { api, features } from '../../api.ts'
  import { ownerAvatar, ownerName, setOwnerProfile } from '../settings/owner.ts'
  // The owner's bar, like Discord's user panel: the owner's avatar and name,
  // which open a small editor, then Settings, Notifications and Sign out.
  // At the foot of the sidebar it is one row. When the sidebar is hidden, the
  // rail shows it as a column of icons with the avatar last, so each control
  // still has exactly one place on the page.
  interface Props {
    hue?: number
    route?: RouteNotice | null
    /** An update is ready; the settings button shows a dot. */
    update?: boolean
    vertical?: boolean
    onsettings: () => void
    onlogout: () => void
  }
  let { hue = 0, route = null, update = false, vertical = false, onsettings, onlogout }: Props = $props()
  const emojis = ['🦊', '🐙', '🦉', '🐝', '🌵', '🚀', '🎧', '☕']

  let bar = $state<HTMLElement>()
  let nameInput = $state<HTMLInputElement>()
  let editing = $state(false)
  let at = $state({ left: 0, bottom: 0 })
  let name = $state('')
  let avatar = $state('')
  let busy = $state(false)
  let note = $state('')

  function edit() {
    const box = bar?.getBoundingClientRect()
    if (box) at = vertical ? { left: box.right + 8, bottom: 8 } : { left: box.left + 8, bottom: window.innerHeight - box.top + 8 }
    name = ownerName() === 'you' ? '' : ownerName()
    avatar = ownerAvatar()
    note = ''
    editing = true
  }

  async function save(e: SubmitEvent) {
    e.preventDefault()
    busy = true
    note = ''
    try {
      setOwnerProfile(await api.setOwnerProfile({ name, avatar }))
      editing = false
    } catch (err) {
      note = errorText(err)
    }
    busy = false
  }
</script>

<footer class="me" class:vertical bind:this={bar}>
  <button class="who" onclick={edit} aria-haspopup="dialog" aria-expanded={editing} aria-label="{ownerName()}, edit your name and avatar" title="Edit your name and avatar">
    <Avatar avatar={ownerIdentity} name={ownerName()} {hue} size={32} dotGap="#18171a" />
    {#if !vertical}
      <span class="text">
        <strong>{ownerName()}</strong>
        {#if route?.better}<span class="route">Nearer route: {route.better}</span>{/if}
      </span>
    {/if}
  </button>
  <div class="tools">
    <button class="tool" onclick={onsettings} aria-label={update ? 'Settings, update available' : 'Settings'} title={update ? 'Settings · update available' : 'Settings'}>
      <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path d="M12 15.2a3.2 3.2 0 1 0 0-6.4 3.2 3.2 0 0 0 0 6.4Z" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="M19.4 13.5a7.6 7.6 0 0 0 0-3l2-1.6-2-3.4-2.4 1a7.4 7.4 0 0 0-2.6-1.5L14 2.5h-4l-.4 2.5A7.4 7.4 0 0 0 7 6.5l-2.4-1-2 3.4 2 1.6a7.6 7.6 0 0 0 0 3l-2 1.6 2 3.4 2.4-1a7.4 7.4 0 0 0 2.6 1.5l.4 2.5h4l.4-2.5a7.4 7.4 0 0 0 2.6-1.5l2.4 1 2-3.4Z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" /></svg>
      {#if update}<span class="udot"></span>{/if}
    </button>
    {#if features.push}<PushBell />{/if}
    <button class="tool" onclick={onlogout} aria-label="Sign out this device" title="Sign out this device">
      <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path d="M14 4.5H6.5a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2H14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /><path d="M11 12h9m-3.2-3.5L20.3 12l-3.5 3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
    </button>
  </div>
</footer>

{#if editing}
  <div class="backdrop">
    <div class="editor" style="left: {at.left}px; bottom: {at.bottom}px" role="dialog" aria-modal="true" aria-label="Your name and avatar" tabindex="-1" use:dialog={{ close: () => (editing = false), busy, initial: () => nameInput }}>
      <form onsubmit={save}>
        <div class="preview">
          <Avatar avatar={{ kind: 'unknown', emoji: avatar.trim(), room: '', color: '' }} name={name.trim() || 'you'} {hue} size={48} dotGap="var(--field)" />
          <label class="field">
            <span>Name</span>
            <input bind:this={nameInput} bind:value={name} maxlength="32" placeholder="you" autocomplete="nickname" />
          </label>
        </div>
        <fieldset>
          <legend>Avatar</legend>
          <div class="picks">
            <button type="button" class="pick none" class:on={!avatar} onclick={() => (avatar = '')} aria-pressed={!avatar} aria-label="Use initials" title="Initials">Aa</button>
            {#each emojis as e (e)}
              <button type="button" class="pick" class:on={avatar === e} onclick={() => (avatar = e)} aria-pressed={avatar === e} aria-label="Use {e}">{e}</button>
            {/each}
          </div>
          <label class="field">
            <span>Or type one emoji</span>
            <input bind:value={avatar} maxlength="16" placeholder="🙂" autocomplete="off" />
          </label>
        </fieldset>
        {#if note}<p class="note" role="alert">{note}</p>{/if}
        <div class="actions">
          <button type="button" class="btn quiet" onclick={() => (editing = false)} disabled={busy}>Cancel</button>
          <button type="submit" class="btn fill" disabled={busy}>Save</button>
        </div>
      </form>
    </div>
  </div>
{/if}

<style>
  .me {
    height: 56px;
    flex: none;
    background: #18171a;
    display: flex;
    align-items: center;
    gap: var(--s1);
    padding: 0 var(--s2);
  }
  .who {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: var(--s2);
    padding: var(--s1);
    border-radius: 6px;
    text-align: left;
    transition: background-color var(--motion-fast) var(--ease);
  }
  @media (hover: hover) {
    .who:hover { background: var(--hover); }
  }
  .who:active { background: var(--select); }
  .text { display: flex; flex-direction: column; min-width: 0; line-height: var(--leading-tight); }
  strong { font-size: var(--t-body); font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .route { font-size: var(--t-cap); color: var(--faint); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .tools { display: flex; align-items: center; flex: none; }
  .tool { position: relative; width: 32px; height: 32px; border-radius: 6px; display: grid; place-items: center; color: var(--muted); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .tool:hover { background: var(--hover); color: var(--ink); }
  }
  .tool:active { background: var(--select); }
  .udot { position: absolute; top: 4px; right: 4px; width: 8px; height: 8px; border-radius: 50%; background: var(--gold); box-shadow: 0 0 0 2px #18171a; }

  .me.vertical { height: auto; flex-direction: column-reverse; background: none; padding: 0; gap: var(--s1); }
  .me.vertical .who { flex: none; padding: 4px; }
  .me.vertical .tools { flex-direction: column; gap: var(--s1); }
  .me.vertical .tool { width: 40px; height: 40px; border-radius: 8px; }
  .me.vertical .udot { top: 6px; right: 6px; box-shadow: 0 0 0 2px var(--rail); }
  .me.vertical :global(.bell) { width: 40px; height: 40px; border-radius: 8px; }
  /* On the rail, the bell's panel opens to the right instead of above. */
  .me.vertical :global(.panel) { right: auto; left: calc(100% + 12px); bottom: 0; }

  .backdrop { position: fixed; inset: 0; z-index: 40; }
  .editor {
    position: fixed;
    width: min(288px, calc(100vw - 16px));
    padding: var(--s3);
    border-radius: 8px;
    background: var(--field);
    box-shadow: 0 6px 24px rgba(0, 0, 0, 0.45);
  }
  form {
    display: flex;
    flex-direction: column;
    gap: var(--s3);
  }
  .preview { display: flex; align-items: flex-end; gap: var(--s3); }
  .field { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: var(--s1); font-size: var(--t-sm); color: var(--muted); }
  input { height: 32px; padding: 0 var(--s2); border: 1px solid var(--line); border-radius: 6px; background: var(--main); color: var(--ink); font: inherit; font-size: var(--t-ui); }
  input:focus { border-color: var(--iris-edge); outline: none; }
  fieldset { margin: 0; padding: 0; border: 0; display: flex; flex-direction: column; gap: var(--s2); }
  legend { padding: 0; margin-bottom: var(--s2); font-size: var(--t-sm); color: var(--muted); }
  .picks { display: flex; flex-wrap: wrap; gap: var(--s1); }
  .pick { width: 30px; height: 30px; border-radius: 6px; display: grid; place-items: center; font-size: var(--t-head); line-height: 1; transition: background-color var(--motion-fast) var(--ease); }
  .pick.none { font-size: var(--t-cap); font-weight: 600; color: var(--muted); }
  @media (hover: hover) {
    .pick:hover { background: var(--hover); }
  }
  .pick:active { background: var(--select); }
  .pick.on { background: var(--select); box-shadow: inset 0 0 0 1px var(--iris-edge); }
  .note { margin: 0; font-size: var(--t-sm); color: var(--coral); }
  .actions { display: flex; justify-content: flex-end; gap: var(--s2); }
</style>

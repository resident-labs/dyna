<script lang="ts">
  import type { UpdateNotice } from '../../api/settings.ts'
  import type { EngineStatus } from '../../engineStatus.ts'
  // The lines above the open view: the drop target while files come over the
  // window, why the engine does not answer, that posting is off, a newer
  // release, and the room's last load error. App keeps their state and the
  // drop handlers; this component is the region that shows them.
  interface Props {
    dropping: boolean
    /** Where a dropped file goes: the open session, or the room. */
    dropTarget: string
    engineStatus: EngineStatus
    health: string
    update: UpdateNotice | null
    updateHidden: boolean
    roomId: string
    loadError: string
    onupdatehidden: () => void
  }
  let { dropping, dropTarget, engineStatus, health, update, updateHidden, roomId, loadError, onupdatehidden }: Props = $props()
</script>

{#if dropping}
  <div class="dropzone"><div><strong>Drop to attach</strong><span>to your next post in {dropTarget}</span></div></div>
{/if}
{#if engineStatus}
  <p class="engine-down" class:waiting={engineStatus === 'reconnecting'} role={engineStatus === 'reconnecting' ? 'status' : 'alert'}><strong>Reconnecting…</strong>
    {#if engineStatus === 'reconnecting'}Contact with the engine broke. Dyna tries again now, and posts wait until it answers.
    {:else if engineStatus === 'offline'}This device is offline. Dyna reconnects when its network is back.
    {:else if engineStatus === 'unreachable'}Dyna cannot reach the engine. Check this device's network connection.
    {:else if engineStatus === 'engine'}The engine did not answer. Agents, posts and merges wait until it is back. On the engine's computer, run <code>systemctl --user start dyna-engine</code>.
    {/if}</p>
{/if}
{#if health}
  <p class="notice">Posting is off: {health}</p>
{/if}
{#if update && !updateHidden && roomId}
  <p class="update">{update.text} <button onclick={onupdatehidden}>Dismiss</button></p>
{/if}
{#if loadError}
  <p class="load-err">{loadError}</p>
{/if}

<style>
  .dropzone {
    position: absolute;
    inset: 12px;
    z-index: 20;
    border: 2px dashed var(--iris-edge);
    border-radius: 12px;
    background: rgba(28, 27, 30, 0.88);
    display: grid;
    place-items: center;
    pointer-events: none;
  }
  .dropzone div { display: flex; flex-direction: column; align-items: center; gap: 4px; }
  .dropzone strong { font-size: 18px; font-weight: 600; }
  .dropzone span { color: var(--muted); font-size: 14px; }
  .notice { color: var(--gold); padding: 8px 16px; margin: 0; font-size: 13px; border-bottom: 1px solid var(--line-soft); }
  .load-err { color: var(--coral); padding: 8px 16px; margin: 0; font-size: 13px; }
  .engine-down { color: var(--coral); padding: 8px 16px; margin: 0; font-size: 13px; border-bottom: 1px solid var(--line-soft); }
  .engine-down.waiting { color: var(--muted); }
  .engine-down strong { font-weight: 600; margin-right: 4px; }
  .update { color: var(--muted); padding: 7px 16px; margin: 0; font-size: 12.5px; border-bottom: 1px solid var(--line-soft); background: var(--gold-wash); }
  .update button { color: var(--faint); font-size: 12px; margin-left: 8px; text-decoration: underline; transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .update button:hover { color: var(--ink); }
  }
</style>

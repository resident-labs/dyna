<script lang="ts">
  import { sessionStatus } from '../sessions/status.ts'
  import { initials } from './format.ts'

  interface Props {
    name?: string
    hue?: number
    size?: number
    // state is the member's session state; the dot draws it.
    state?: string
    status?: string
    // ring is the color of the gap between the avatar and its dot.
    ring?: string
    owner?: boolean
    emoji?: string
    // image is an icon URL, such as a harness logo.
    image?: string
  }
  let { name = '', hue = 0, size = 40, state = '', status = '', ring = 'var(--main)', owner = false, emoji = '', image = '' }: Props = $props()
</script>

<span class="avatar bg-{hue}" class:owner class:has-image={!!image} style="--s:{size}px; --ring:{ring}">
  {#if image}
    <img class="image" src={image} alt="" width={Math.round(size * 0.62)} height={Math.round(size * 0.62)} draggable="false" />
  {:else if emoji && !owner}
    <span class="glyph" aria-hidden="true">{emoji}</span>
  {:else}
    <span class="letters">{owner ? name : initials(name)}</span>
  {/if}
  {#if state}
    <span class="dot {state}" title={sessionStatus({ state, status })}></span>
  {/if}
</span>

<style>
  .avatar {
    position: relative;
    flex: none;
    width: var(--s);
    height: var(--s);
    border-radius: 50%;
    display: inline-grid;
    place-items: center;
    background: color-mix(in srgb, var(--h) 20%, #1d1c1f);
    color: var(--h);
  }
  .letters {
    font-weight: 600;
    font-size: calc(var(--s) * 0.36);
    letter-spacing: 0.01em;
    line-height: 1;
  }
  .image { display: block; object-fit: contain; }
  .avatar.has-image { background: var(--raise); }
  .glyph {
    font-family: 'Noto Color Emoji', 'Apple Color Emoji', 'Segoe UI Emoji', sans-serif;
    font-size: calc(var(--s) * 0.56);
    line-height: 1;
  }
  .owner .letters { font-size: calc(var(--s) * 0.3); }
  .dot {
    position: absolute;
    right: -2px;
    bottom: -2px;
    width: calc(var(--s) * 0.34);
    height: calc(var(--s) * 0.34);
    min-width: 10px;
    min-height: 10px;
    border-radius: 50%;
    border: 3px solid var(--ring);
    background: var(--faint);
  }
  .dot.live { background: var(--gold); }
  .dot.done { background: var(--sage); }
  .dot.error { background: var(--coral); }
  .dot.finished { background: var(--coral); }
  .dot.idle { background: #77736b; }
  .dot.closed { background: transparent; border-width: 3px; box-shadow: inset 0 0 0 2px #5e5a54; }
</style>

<script lang="ts">
  // One step of the Android pairing wizard: a single QR with a hint, and the step's buttons.
  // Step 1 (install) shows the download link. A browser cannot install an app and open it in one
  // link, so step 2 (pair) shows the dyna://pair link that opens the installed app and pairs it.
  import { qrPath } from './qr.ts'

  interface Props {
    step: 'install' | 'pair'
    link: string
    note?: string // under the pair QR: the code and how long it works
    expired?: boolean
    onnext: () => void
    onback: () => void
    onmake: () => void
  }
  let { step, link, note = '', expired = false, onnext, onback, onmake }: Props = $props()

  let qr = $derived(qrPath(link))
</script>

<div class="android">
  <div class="qr" class:dim={expired}>
    <svg viewBox="-3 -3 {qr.size + 6} {qr.size + 6}" role="img" aria-label={step === 'install' ? 'QR code to download the app' : 'QR code to pair the app'} shape-rendering="crispEdges">
      <rect x="-3" y="-3" width={qr.size + 6} height={qr.size + 6} fill="#fff" rx="2" />
      <path d={qr.d} fill="#151416" />
    </svg>
    {#if expired}<button class="btn fill lg again" onclick={onmake}>Make a new code</button>{/if}
  </div>
  {#if step === 'install'}
    <p class="hint">Scan this on the phone and install the app.</p>
    <span class="row">
      <button class="btn" onclick={onback}>Back</button>
      <a class="btn" href={link} download="dyna-call.apk">Download link</a>
      <button class="btn fill" onclick={onnext}>Next</button>
    </span>
  {:else}
    <p class="hint">Scan this to open the app and pair it. {note}</p>
    <span class="row">
      <button class="btn" onclick={onback}>Back</button>
      <a class="btn" href={link}>Pair link</a>
    </span>
  {/if}
</div>

<style>
  .android { display: flex; flex-direction: column; align-items: center; gap: var(--s3); background: var(--main); border: 1px solid var(--line-soft); border-radius: 12px; padding: var(--s4); }
  .qr { position: relative; width: 176px; height: 176px; }
  .qr svg { width: 100%; height: 100%; display: block; border-radius: 8px; }
  .qr.dim svg { opacity: 0.18; }
  .again { position: absolute; inset: 0; margin: auto; width: max-content; height: max-content; }
  .hint { margin: 0; font-size: var(--t-body); color: var(--muted); text-align: center; }
  .row { display: flex; gap: var(--s2); }
  a.btn { text-decoration: none; }
  @media (max-width: 760px) {
    .qr { width: 200px; height: 200px; }
    .btn { min-height: 44px; }
  }
</style>

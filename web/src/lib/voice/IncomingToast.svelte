<script lang="ts">
  // Root asked to talk now (dyna call). The request's reason says why.
  // Accept starts the call in that room; Decline closes the request.
  import type { CallRequest } from './callRequests.ts'
  import Avatar from '../ui/Avatar.svelte'

  // narrow: a heads-up card at the top of a phone screen, with touch-sized
  // buttons. full: a push tap opened the app; the screen covers the app and
  // Accept is the tap a browser needs before it opens the microphone.
  let { request, onaccept, ondecline, narrow = false, full = false }: {
    request: CallRequest
    onaccept: () => void
    ondecline: () => void
    narrow?: boolean
    full?: boolean
  } = $props()
</script>

<div class="incoming" class:narrow class:full role="alertdialog" aria-label="root would like to speak with you">
  <div class="top">
    <span class="ring"><Avatar name="Voice" hue={3} size={full ? 72 : 40} ring="var(--field)" /></span>
    <div class="who">
      <strong>root would like to speak with you</strong>
      <p>{request.reason}</p>
    </div>
  </div>
  <div class="row">
    <button class="btn fill" onclick={onaccept}>Accept</button>
    <button class="btn danger" onclick={ondecline}>Decline</button>
  </div>
</div>

<style>
  .incoming {
    position: fixed;
    z-index: 51;
    right: 12px;
    bottom: 16px;
    width: min(296px, calc(100vw - 24px));
    background: var(--field);
    border: 1px solid var(--line);
    border-radius: var(--r-sm);
    box-shadow: 0 6px 24px rgba(0, 0, 0, 0.45);
    padding: var(--s3);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .top { display: flex; gap: var(--s3); align-items: center; }
  .ring { display: inline-grid; border-radius: 50%; box-shadow: 0 0 0 2px var(--field), 0 0 0 4px var(--gold); animation: live 1s ease-in-out infinite; }
  @keyframes live { 50% { opacity: 0.6; } }
  .who { min-width: 0; }
  .who strong { display: block; font-size: var(--t-body); font-weight: 600; }
  .who p { margin: 0; font-size: var(--t-sm); line-height: var(--leading); color: var(--muted); overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 4; line-clamp: 4; -webkit-box-orient: vertical; overflow: hidden; }
  .row { display: flex; gap: var(--s2); }
  /* A phone: the card docks above the composer like the app's toast, so the
     top bar and its buttons stay usable while it rings. */
  .narrow {
    top: auto;
    bottom: calc(96px + env(safe-area-inset-bottom));
    left: calc(8px + env(safe-area-inset-left));
    right: calc(8px + env(safe-area-inset-right));
    width: auto;
  }
  .narrow .who p { -webkit-line-clamp: 2; line-clamp: 2; }
  .narrow .row .btn { flex: 1; min-height: 44px; font-size: var(--t-body); }
  .full {
    inset: 0;
    width: auto;
    border: 0;
    border-radius: 0;
    justify-content: center;
    align-items: center;
    text-align: center;
    padding: 24px;
    gap: 24px;
    z-index: 60;
  }
  .full .top { flex-direction: column; }
  .full .who p { -webkit-line-clamp: 8; line-clamp: 8; font-size: var(--t-body); }
  .full .row { width: min(360px, 100%); }
  .full .row .btn { flex: 1; min-height: 56px; font-size: var(--t-body); }
  @media (prefers-reduced-motion: reduce) { .ring { animation: none; } }
</style>

<script lang="ts">
  // A post Dyna has not accepted after 10 s says "Sending…", so a slow or stalled engine shows as waiting and not as
  // nothing. It is not a failure and offers no retry: a slow acceptance still goes through. One timer, no ticking.
  let { at }: { at: string } = $props();
  let slow = $state(false);
  $effect(() => {
    const wait = Date.parse(at) + 10_000 - Date.now();
    if (wait <= 0) { slow = true; return; }
    slow = false;
    const timer = setTimeout(() => { slow = true; }, wait);
    return () => clearTimeout(timer);
  });
</script>

{#if slow}<p class="slow-send" role="status">Sending…</p>{/if}

<style>
  .slow-send { margin: 0; color: var(--muted); font-size: 12.5px; line-height: 1.25; }
</style>

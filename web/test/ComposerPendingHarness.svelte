<script lang="ts">
  import Composer from '../src/lib/rooms/Composer.svelte'

  let composerWork = $state<Record<string, boolean>>({})
  let composerPending = $derived(Object.values(composerWork).some(Boolean))
  function noteComposerWork(key: string, pending: boolean) {
    composerWork[key] = pending
  }
</script>

<Composer room={{ id: 'r', name: 'room' }} onsend={() => {}} onpending={(pending) => noteComposerWork('feed:r', pending)} />
<output aria-label="Composer pending">{composerPending}</output>

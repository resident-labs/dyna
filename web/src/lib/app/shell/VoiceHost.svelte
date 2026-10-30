<script lang="ts">
  import VoiceWidget from '../../voice/VoiceWidget.svelte'
  import { callRequests } from '../../voice/voiceCall.ts'
  import type { AppEvent } from '../../api/event.ts'
  import type { Attachment, Room } from '../../api/room.ts'
  import type { CallRequest } from '../../voice/callRequests.ts'

  let { roomId, narrow, rooms, enabled, calls = $bindable([]), onopenfile }: {
    roomId: string
    narrow: boolean
    rooms: Room[]
    enabled: boolean
    calls?: CallRequest[]
    onopenfile: (attachment: Attachment & { roomId?: string; postId?: string }) => void
  } = $props()

  let widget = $state<ReturnType<typeof VoiceWidget>>()
  let link = $state<{ room: string; call: string } | null>(null)
  $effect(() => { calls = $callRequests.filter((request) => request.room === roomId) })
  $effect(() => { if (roomId) callRequests.load(roomId).catch(() => {}) })

  export function openLink(target: { room: string; call: string } | null) {
    link = target ?? link
  }
  export function event(ev: AppEvent | { kind: 'room.paused' | 'room.resumed'; roomId?: string }) {
    widget?.event(ev)
  }
  export function accept(request: CallRequest) { widget?.accept(request) }
  export function decline(request: CallRequest) { widget?.decline(request) }
</script>

{#if enabled}
  <VoiceWidget bind:this={widget} bind:link {narrow} {rooms} {onopenfile} />
{/if}

<script lang="ts">
 import SessionThread from "../src/lib/ui/SessionThread.svelte";
 import { RoomStore } from "../src/lib/room.svelte.ts";
 const store = new RoomStore("r");
</script>
<div style="height: 780px; display: flex; min-height: 0;">
 <SessionThread session="crew" roomName="horizon" {store} operate={false} unsent={[]} hasRoom={()=>true} onunsent={()=>{}} onsettled={()=>{}} onpostlocation={()=>{}} navigate={event=>event.preventDefault()} />
</div>

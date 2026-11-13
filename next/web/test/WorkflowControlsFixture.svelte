<script lang="ts">
 import WorkflowRun from "../src/lib/ui/WorkflowRun.svelte";
 import { RoomStore } from "../src/lib/room.svelte.ts";
 const store = new RoomStore("r");
 store.running = () => true;
</script>
<div style="height: 100vh; display: flex; min-height: 0;">
 <WorkflowRun session="run-session" roomName="dyna" {store} operate={true} hasRoom={()=>true} onpostlocation={()=>{}} navigate={event=>event.preventDefault()} clock={()=>Date.parse("2026-11-13T09:30:00Z")} />
</div>

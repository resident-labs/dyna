<script lang="ts">
 import PostRow from "../src/lib/ui/PostRow.svelte";
 import PostData from "../src/lib/ui/PostData.svelte";
 import { postData } from "../src/lib/post-data.ts";
 import { setSessionLabels } from "../src/lib/ui/Avatar.svelte";
 import { setMentionLinks } from "../src/lib/ui/Markdown.svelte";
 import type { Row } from "../src/lib/feed.ts";
 import type { JSONValue, PostDataRef } from "../src/lib/contract.gen.ts";
 const query = new URLSearchParams(location.search);
 const referenceOnly = query.has("reference"), unrelated = query.has("unrelated");
 let opened = $state<number | undefined>(undefined);
 let reference = $state<PostDataRef>({file:{sha256:"b".repeat(64),name:"data.json",media:"application/json",bytes:250000},summary:"1 keys"});
 setSessionLabels(() => new Map());
 setMentionLinks(() => ({ room: "horizon", sessions: new Map(), open: event => event.preventDefault() }));
 const conflicts = Array.from({ length: 38 }, (_, i) => ({ file: `next/engine/groups/conflict-${i + 1}.go`, reason: i === 0 ? "CONFLICT_SENTINEL · both branches changed the recipient's delivery" : "Both branches edited this file; resolve before merging.", "0": "Object keys stay quoted" }));
 const data = { groups: [{ name: "upstream-sync", branch: "newest-green-sync-0b9hbj", conflicts }, { name: "services", conflicts: [] }], merged: 9, status: "Needs conflict resolution" };
 const values: [string, JSONValue | undefined][] = [
  ["Merged 9 groups. upstream-sync needs conflict resolution; full details are in data.", data],
  ["Small result", { merged: 9, failed: false }], ["Explicit null", null], ["Scalar", 42],
  ["Hostile strings stay text", { hostile: '<' + '/script><img src=x onerror=alert(1)>', unicode: '雪🙂' }],
  ["299 byte boundary", "x".repeat(297)], ["300 byte boundary", "x".repeat(298)], ["301 byte boundary", "x".repeat(299)],
  ["Legacy text fence stays untouched\n\n```json\n{\"plain\": true}\n```", undefined],
  ["Long single string", "LONG_STRING_SENTINEL " + "x".repeat(1600)],
 ];
 const row = (text: string, value: JSONValue | undefined, i: number): Extract<Row, {kind:"post"}> => {
  const bytes = value === undefined ? 0 : postData(value).bytes;
  const large = bytes > 1024;
  return {kind:"post",key:String(i),from:{kind:"session",session:"crew"},at:"2026-11-05T09:46:00Z",text,data:value,dataAttachment:large ? 0 : undefined,
   attachments:large ? [{name:"data.json",media:"application/json",sha256:"a".repeat(64),bytes}, ...unrelated && i === 0 ? [{name:"data.json",media:"application/json",sha256:"c".repeat(64),bytes:17}] : []] : [],first:true,to:[]};
 };
</script>
<main><header><h1>Room feed</h1><span>Workflow data · component fixture</span></header>
 {#if referenceOnly}<PostData {reference} /><button type="button" onclick={() => { reference = {...reference,file:{...reference.file}}; }}>Refresh same post</button>{:else}
 {#each values as [text,value],i}<PostRow row={row(text,value,i)} author="newest-green-sync-0b9hbj" to="upstream-sync" room="horizon" root={false} navigate={event=>event.preventDefault()} onretry={()=>{}} ondiscard={()=>{}} newest={new Map()} visualEvent={undefined} onmedia={position => { opened = position; }} />{/each}{/if}
 {#if unrelated}<output aria-label="Opened attachment">{opened ?? "none"}</output>{/if}
</main>
<style>
 main { max-width: 840px; margin: 32px auto; background: var(--bg); color: var(--text); padding: 0 16px 32px; }
 header { padding: 8px 12px 20px; border-bottom: 1px solid var(--line); margin-bottom: 12px; }
 h1 { font-size: 20px; margin: 0 0 4px; font-weight: 600; } header span { font-size: 13px; color: var(--muted); }
 @media (max-width: 640px) { main { margin: 12px auto; padding: 0 6px 24px; } }
</style>

<script lang="ts">
  import { onMount, type Snippet } from "svelte";
  import { APIError, call, SignedOut } from "../client.ts";
  import { confirm } from "../modal.ts";
  import { count, fileSize } from "../feed.ts";
  import { watchHosts } from "../hosts.ts";
  import { fileList, imageGroup, imageKey, imageLine, imageOrder, imageSummary, imageText, runnerFailures, runnerImage } from "../images.ts";
  import type { HostSummary, ImageShowResponse, RoomImage, RoomSummary } from "../contract.gen.ts";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import ItemList from "./settings/ItemList.svelte";
  import ListDetail from "./settings/ListDetail.svelte";
  // Settings > Images (spec 5.13): every room's images, grouped by room, and the open image's detail, the one place that
  // explains what it is, how a ci-runner image builds on the CI hosts, and what removing it does. image.list is whole, so
  // the list searches here. A ci-runner image's failed builds come from host.list, read every 10 s as Hosts reads it,
  // each host as it reports itself. Images are saved from a box, so the page has no Add. The pane is admin only (routes.ts).
  let { bar, signedout, selected, onselect, onclose }: {
    bar: Snippet<[string, number | undefined, Snippet | undefined]>; signedout: () => void; selected: string | undefined;
    onselect: (key: string, open: boolean) => void; onclose: () => void;
  } = $props();
  let images: RoomImage[] | undefined = $state.raw(), rooms: RoomSummary[] = $state.raw([]), hosts: HostSummary[] = $state.raw([]);
  let failed = $state(false);
  let names = $derived(new Map(rooms.map(room => [room.id, room.name])));
  let items = $derived(images && imageOrder(images, names));
  let item = $derived(items?.find(each => imageKey(each) === selected));
  // The open image's Containerfile and files, for the save the list holds; or that the read failed.
  let shown: { key: string; id: string; reply: ImageShowResponse } | undefined = $state.raw();
  let showFailed = $state(false);

  const gone = (reason: unknown) => { if (reason instanceof SignedOut) signedout(); return reason instanceof SignedOut; };
  const words = (reason: unknown) => reason instanceof APIError ? reason.detail.message : "That did not work. Try again.";
  // The images and the rooms that name their groups come together, so no image shows under a room not yet listed.
  function reload() {
    return Promise.all([call("image.list", {}), call("room.list", {})]).then(([list, listed]) => { rooms = listed.rooms ?? []; images = list.images ?? []; failed = false; },
      (reason: unknown) => { if (!gone(reason) && !images) failed = true; });
  }
  onMount(() => {
    void reload();
    // A failed read keeps the last hosts, and the next one tries again.
    return watchHosts(list => { hosts = list; }, () => {}, list => list);
  });
  function read(image: RoomImage) {
    const key = imageKey(image), id = image.id;
    showFailed = false;
    void call("image.show", { name: image.name, room: image.room }).then(reply => { if (selected === key) shown = { key, id, reply }; },
      (reason: unknown) => { if (!gone(reason) && selected === key) showFailed = true; });
  }
  // An image opened, or saved again, reads its Containerfile once.
  $effect(() => { if (item && (shown?.key !== imageKey(item) || shown.id !== item.id)) read(item); });

  async function remove(image: RoomImage, room: string, opener: HTMLElement) {
    const ok = await confirm({ title: `Remove ${image.name} from ${room}?`,
      message: image.name === runnerImage ? `The CI jobs of ${room} will then use the shared runner image.` : `New boxes in ${room} stop using it. Boxes already running keep what they have.`,
      action: "Remove", danger: true, opener, failed: words, run: () => call("image.remove", { name: image.name, room: image.room }).catch((reason: unknown) => { gone(reason); throw reason; }) });
    if (!ok) return;
    // The engine removed it, so it leaves the list even if the next read fails.
    images = images?.filter(other => imageKey(other) !== imageKey(image));
    onclose();
    await reload();
  }
  const plural = (n: number, one: string, many: string) => `${n.toLocaleString("en")} ${n === 1 ? one : many}`;
</script>

{#snippet row(x: RoomImage)}
  <span class="icon"><Icon name="box" size={20} /></span>
  <span class="lines"><span class="title"><span class="name" title={x.name}>{x.name}</span>{#if x.name === runnerImage}<span class="badge">CI runner</span>{/if}
    {#if runnerFailures(x, hosts).length}<StatusPill tone="warning" label="Build failed" />{/if}</span>
    <span class="line">{imageLine(x)}</span></span>
{/snippet}

{#snippet empty()}<span class="small">A box saves one with dyna image save.</span>{/snippet}

{#snippet list({ drill, select }: { drill: boolean; select: (key: string, open: boolean) => void })}
  <ItemList {items} {failed} retry={() => { failed = false; void reload(); }} key={imageKey} text={imageText(names)} noun={["image", "images"]} label="Images"
    group={imageGroup(names)} {row} {empty} {selected} {drill} onselect={select} />
{/snippet}

{#snippet image(x: RoomImage)}{@const listed = names.get(x.room)}{@const room = listed ?? "a room no longer listed"}{@const failures = runnerFailures(x, hosts)}{@const files = shown?.key === imageKey(x) ? fileList(shown.reply.files) : undefined}
  {#if x.name === runnerImage}<p class="small muted">The CI runner image of {room}: its repositories' jobs run on it. Each CI host builds it when it next needs it, within 10 minutes; where a build fails or takes longer, the room's jobs on that host run on the shared runner image, and the host tries again after 5 minutes. Saved from a box with dyna image save; other rooms do not see it.</p>
  {:else}<p class="small muted">A box image of {room}, built from this folder for the room's new boxes. Saved from a box with dyna image save; other rooms do not see it.</p>{/if}
  <dl class="facts"><dt>Room</dt><dd>{listed ?? "A room no longer listed"}</dd><dt>Size</dt><dd>{count(x.files, "file")}, {fileSize(x.bytes)} unpacked</dd></dl>
  {#if failures.length}<div class="section"><strong class="head">Build failed</strong>
    {#each failures as failure, index (index)}<div class="failure"><span class="host">{failure.host}</span><p class="reason">{failure.reason}</p></div>{/each}</div>{/if}
  {#if shown?.key === imageKey(x) && files}
    <div class="section"><strong class="head">Containerfile</strong><pre class="code">{shown.reply.containerfile}</pre></div>
    <div class="section"><strong class="head">Files</strong><p class="mono">{files.shown.join(" · ")}{files.more ? ` · ${files.more.toLocaleString("en")} more` : ""}</p></div>
  {:else if showFailed}<div class="section"><p class="small muted" role="alert">The Containerfile did not load.</p>
    <div class="actions"><button type="button" class="btn" onclick={() => read(x)}>Try again</button></div></div>
  {:else}<p class="small muted">Loading the Containerfile…</p>{/if}
  <div class="danger-zone"><span class="small muted">{x.name === runnerImage ? "The room's jobs will then use the shared runner image." : "New boxes in this room stop using it."}</span>
    <button type="button" class="btn danger" aria-haspopup="dialog" onclick={event => void remove(x, room, event.currentTarget)}>Remove</button></div>
{/snippet}

{#snippet detail()}
  <div class="detail">{#if item}{#key imageKey(item)}{@render image(item)}{/key}{:else}<p class="muted">That image is gone, or the link is wrong.</p>{/if}</div>
{/snippet}

{#snippet summary()}
  {#if items}{@const sum = imageSummary(items, hosts)}<p class="muted"><span class="fact">{plural(sum.images, "image", "images")}</span> <span class="fact">in {plural(sum.rooms, "room", "rooms")}</span>
    </p>{#if sum.failed}<p class="muted">{plural(sum.failed, "CI runner image", "CI runner images")} failed to build.</p>{/if}
    <p class="small muted">Rooms with no image have no group.</p>{/if}
{/snippet}

{#snippet header()}{@render bar("Images", items?.length, undefined)}{/snippet}

<ListDetail bar={header} empty={items?.length === 0} label="Images" title={item ? item.name : "Not found"} open={!!items && selected !== undefined} {onselect} {onclose} {list} {detail} {summary} />

<style>
  .icon { display: grid; flex: none; color: var(--muted); }
  .detail { display: grid; gap: 18px; min-width: 0; overflow-wrap: anywhere; }
  .head { font-size: 14px; font-weight: 600; }
  .failure { display: grid; gap: 4px; padding: 10px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--raised); }
  .host { color: var(--attention); font-size: 13px; font-weight: 600; }
  .reason { font-family: var(--mono); font-size: 12px; line-height: 1.5; }
  .code { margin: 0; padding: 10px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--raised); font: 12.5px/1.5 var(--mono); overflow-x: auto; white-space: pre; }
  .mono { font-family: var(--mono); font-size: 12.5px; line-height: 1.6; }
</style>

<script lang="ts">
  import { onMount, type Snippet } from "svelte";
  import { APIError, call, SignedOut } from "../client.ts";
  import { profileChips, profileForSave, profileColumns, profileLine, profileModels, profileRows, profileText, providerFilter, providers, unrated, type ProfileModel, type ProfileSummary } from "../profiles.ts";
  import type { ModelProvider } from "../contract.gen.ts";
  import ItemList from "./settings/ItemList.svelte";
  import ListDetail from "./settings/ListDetail.svelte";
  import ProfileEditor from "./ProfileEditor.svelte";
  // Settings > Profiles (spec 5.13): every profile as a compact row of values, and the open profile's editor beside the
  // list, the one place that explains the ratings. New profile opens an empty editor in the detail's place.
  // profile.list is whole, so the list searches, filters and sorts here.
  let { bar, phone, signedout, selected, onselect, onclose }: {
    bar: Snippet<[string, number | undefined, Snippet | undefined]>; phone: boolean; signedout: () => void;
    selected: string | undefined; onselect: (name: string, open: boolean) => void; onclose: () => void;
  } = $props();
  let profiles: ProfileSummary[] | undefined = $state.raw();
  let models: ProfileModel[] = $state.raw([]);
  let failed = $state(false), creating = $state(false), busy = $state(false), error = $state(""), said = $state("");
  let provider: ModelProvider | "" = $state("");
  let profile = $derived(profiles?.find(each => each.name === selected));
  const blank: ProfileSummary = { inUse: 0, name: "", provider: "anthropic", model: "", thinking: "medium", description: "", taste: 0, diagnostics: 0, intelligence: 0, cost: 0 };

  // Resolves once the list has loaded or failed.
  function reload() {
    // Without the accounts the owner can still type a model ID.
    void call("accounts.list", {}).then(result => { models = profileModels(result.accounts ?? []); }, () => {});
    return call("profile.list", {}).then(result => { profiles = profileRows(result.profiles); failed = false; },
      (reason: unknown) => { if (reason instanceof SignedOut) signedout(); else if (!profiles) failed = true; });
  }
  function refresh() { void reload(); }
  // New profile is a step in history of its own, so Back leaves the empty editor as the back arrow does.
  function popped() { if (creating && !history.state?.creating) { creating = false; error = ""; } }
  onMount(() => {
    refresh();
    window.addEventListener("focus", refresh);
    window.addEventListener("popstate", popped);
    return () => { window.removeEventListener("focus", refresh); window.removeEventListener("popstate", popped); };
  });
  // Another profile, or none, starts without the last one's error.
  $effect.pre(() => { void selected; error = ""; said = ""; });

  function showError(reason: unknown, action: "save" | "remove") {
    if (reason instanceof SignedOut) signedout();
    else if (reason instanceof APIError && reason.detail.code === "profile_exists") error = "A profile with this name already exists.";
    else if (reason instanceof APIError && action === "remove" && reason.detail.code === "conflict") { error = "Sessions started using this profile. Remove it again to move them."; refresh(); }
    else error = `Could not ${action} this profile. Try again.`;
  }
  async function save(draft: ProfileSummary) {
    if (busy) return;
    busy = true; error = ""; said = "";
    const create = creating, profile = profileForSave(draft);
    await call("profile.set", { create, profile }).then(async () => {
      await reload();
      if (!create) { said = "Saved."; return; }
      // The new profile opens once it is listed; a reload that failed lists it from what was saved.
      if (!profiles?.some(each => each.name === profile.name)) profiles = [...profiles ?? [], { ...profile, inUse: 0 }].sort((a, b) => a.name.localeCompare(b.name));
      creating = false;
      // The profile takes the history step New profile made, so Back returns to the list.
      if (history.state?.creating) history.replaceState({ fromList: true }, "", location.href);
      onselect(profile.name, false);
    }, (reason: unknown) => showError(reason, "save"));
    busy = false;
  }
  async function remove(name: string, replacement?: string) {
    if (busy) return;
    busy = true; error = "";
    await call("profile.remove", { name, replacement }).then(() => { onclose(); refresh(); }, (reason: unknown) => showError(reason, "remove"));
    busy = false;
  }
  function start() {
    error = ""; said = "";
    if (selected !== undefined) onclose();
    if (!creating) history.pushState({ ...history.state, creating: true }, "", location.href);
    creating = true;
  }
  function close() {
    if (!creating) { onclose(); return; }
    if (history.state?.creating) history.back();
    else { creating = false; error = ""; }
  }
  function select(name: string, open: boolean) { creating = false; onselect(name, open); }
  const value = (n: number | undefined) => n || "–";
</script>

{#snippet row(profile: ProfileSummary, compact: boolean)}
  <span class="lines"><span class="title"><span class="name">{profile.name}</span></span><span class="line">{profileLine(profile, compact)}</span></span>
  {#if !compact}{#each [profile.taste, profile.diagnostics, profile.intelligence, profile.cost] as rating, index (index)}<span class="cell num" class:high={rating >= 9}>{value(rating)}</span>{/each}
    <span class="cell num">{value(profile.maxConcurrent)}</span><span class="cell num">{value(profile.inUse)}</span>{/if}
{/snippet}

{#snippet tools()}
  <select class="provider" aria-label="Provider" bind:value={provider} disabled={!profiles}>
    <option value="">Every provider</option>{#each Object.entries(providers) as [id, name] (id)}<option value={id}>{name}</option>{/each}
  </select>
{/snippet}

{#snippet empty()}<button type="button" class="btn" onclick={start}>New profile</button>{/snippet}

{#snippet list({ drill, select: pick }: { drill: boolean; select: (key: string, open: boolean) => void })}
  <ItemList items={profiles} {failed} retry={() => { failed = false; refresh(); }} key={each => each.name} text={profileText} noun={["profile", "profiles"]} label="Profiles"
    chips={profileChips} columns={profileColumns} sort={{ column: "name", dir: 1 }} filter={providerFilter(provider)} filtered={provider ? providers[provider] : undefined} onclear={() => { provider = ""; }}
    {row} {tools} {empty} selected={creating ? undefined : selected} {drill} onselect={pick} />
{/snippet}

{#snippet detail()}
  {#if creating}{#key "new"}<ProfileEditor profile={blank} profiles={profiles ?? []} {models} isNew {busy} {error} note="" {save} remove={() => {}} />{/key}
  {:else if profile}{#key profile.name}<ProfileEditor {profile} profiles={profiles ?? []} {models} isNew={false} {busy} {error} note={said} {save} remove={replacement => void remove(profile!.name, replacement)} />{/key}
  {:else}<p class="muted">That profile is gone, or the link is wrong.</p>
  {/if}
{/snippet}

{#snippet summary()}
  {#if profiles}<p class="muted"><span class="fact">{profiles.length.toLocaleString("en")} {profiles.length === 1 ? "profile" : "profiles"}</span>
    <span class="fact">· {profiles.filter(unrated).length.toLocaleString("en")} not rated</span> <span class="fact">· {profiles.filter(each => each.inUse > 0).length.toLocaleString("en")} in use</span></p>{/if}
{/snippet}

{#snippet action()}<button type="button" class="btn new" onclick={start}>{phone ? "New" : "New profile"}</button>{/snippet}
{#snippet header()}{@render bar("Profiles", profiles?.length, action)}{/snippet}

<ListDetail bar={header} label="Profiles" title={creating ? "New profile" : profile?.name ?? "Not found"} open={creating || (!!profiles && selected !== undefined)} onselect={select} onclose={close} {list} {detail} {summary} />

<style>
  .new { margin-left: auto; }
  .provider { height: 30px; padding: 0 8px; border: 1px solid var(--firm); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; font-size: 13px; }
  @media (hover: none) { .provider { height: 36px; } }
  .num { color: var(--muted); font-size: 13px; font-variant-numeric: tabular-nums; }
  .num.high { color: var(--text); font-weight: 600; }
</style>

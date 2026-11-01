<script lang="ts">
  import { call } from "../client.ts";
  import { accountChoiceName, accountChoiceNote } from "../accounts.ts";
  import type { SessionAccountResponse } from "../contract.gen.ts";
  import Icon from "./Icon.svelte";
  import MenuList from "./MenuList.svelte";
  // A session's account, and the menu that chooses one, with ProfileMenu's keys. Automatic is the default. A choice that
  // cannot serve right now stays chosen, and the row says the session is on Automatic until it can. The value moves only
  // once the engine has changed it.
  let { session, account, switchable, onchanged }: { session: string; account: SessionAccountResponse; switchable: boolean; onchanged: () => void } = $props();
  let open = $state(false), busy = $state(false), failure = $state("");
  let root: HTMLDivElement;
  let button: HTMLButtonElement | undefined = $state();
  let rows = $derived([{ id: "", label: "Automatic" }, ...account.choices ?? []]);
  let name = $derived(accountChoiceName(account));
  let note = $derived(accountChoiceNote(account.issue));
  function show(next: boolean, refocus = false) {
    if (busy) return;
    open = next;
    if (!open && refocus) button?.focus();
  }
  async function choose(id: string) {
    show(false, true);
    if (id === (account.account ?? "")) return;
    busy = true;
    failure = "";
    await call("session.set_account", { session, account: id }).then(onchanged, () => { failure = "The account did not change. Try again."; });
    busy = false;
  }
</script>

<svelte:window onpointerdown={event => { if (open && event.target instanceof Node && !root.contains(event.target)) show(false); }} />
<div class="menu" bind:this={root}>
  {#if !switchable}<span class="names">{name}</span>{:else}
  <button type="button" class="open" bind:this={button} aria-disabled={busy} aria-haspopup="menu" aria-expanded={open} aria-label={`Account: ${name}. Change account`}
    onclick={() => show(!open)} onkeydown={event => { if (event.key === "ArrowDown") { event.preventDefault(); show(true); } }}>
    <span class="names">{name}</span><Icon name="chevron" /></button>{/if}
  {#if open}
    <MenuList label="Accounts" items={rows} keyOf={entry => entry.id} checked={account.account ?? ""} anchor={root} onchoose={id => void choose(id)} onclose={refocus => show(false, refocus)}>
      {#snippet row(entry)}<span class="names">{entry.label}</span>{/snippet}
    </MenuList>
  {/if}
</div>
{#if note}<small>{note}</small>{/if}
{#if failure}<p role="alert">{failure}</p>{/if}

<style>
  .menu { position: relative; }
  .open { display: inline-flex; align-items: start; gap: 4px; max-width: 100%; margin: 0 -4px; padding: 0 4px; border: 0; border-radius: 4px; background: none; color: var(--text); font: inherit; text-align: left; cursor: pointer; }
  .open :global(svg) { flex: none; width: 14px; height: 14px; margin-top: 3px; color: var(--muted); }
  .names { display: grid; grid-template-columns: minmax(0, 1fr); min-width: 0; overflow-wrap: anywhere; }
  small { display: block; color: var(--muted); font-size: 12.5px; line-height: 18px; }
  .open:hover:not([aria-disabled="true"]), .open[aria-expanded="true"] { background: var(--hover); }
  .open[aria-disabled="true"] { opacity: 0.6; cursor: default; }
  .open:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  p { margin: 4px 0 0; color: var(--danger); font-size: 12.5px; line-height: 1.3; }
</style>

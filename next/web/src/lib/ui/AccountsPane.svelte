<script lang="ts">
  import { onMount, type Snippet } from "svelte";
  import { call, SignedOut } from "../client.ts";
  import { accountRows, accountState, providerGroup, refused, reconnectUnavailable } from "../accounts.ts";
  import { visibleWindows } from "../usage.ts";
  import type { Account, AccountProviderInfo, AccountUsage, AccountsConnectResponse, RoomSummary } from "../contract.gen.ts";
  import StatusPill from "./StatusPill.svelte";
  import Icon from "./Icon.svelte";
  import AccountMeter from "./AccountMeter.svelte";

  let { bar, phone, signedout, path, navigate }: { bar: Snippet<[string, number | undefined, Snippet | undefined]>; phone: boolean; signedout: () => void; path: string; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void } = $props();
  let providers: AccountProviderInfo[] = $state([]), accounts: Account[] = $state([]), rooms: RoomSummary[] = $state([]), usage: AccountUsage[] = $state([]);
  let loaded = $state(false), failed = $state(false), busy = $state(false);
  let query = $state(""), expanded = $state(false), label = $state(""), secret = $state(""), redirect = $state("");
  let keyFlow = $state(false);
  let attempt: AccountsConnectResponse | undefined = $state();
  let connectState: "idle" | "pending" | "failed" | "expired" = $state("idle");
  let reconnectId: string | undefined = $state(), error = $state(""), copied = $state(false), opened = $state(false), now = $state(Date.now());
  let timer: ReturnType<typeof setTimeout> | undefined, generation = 0;
  const base = "/settings/accounts";
  let adding = $derived(path === base + "/add" || path.startsWith(base + "/add/"));
  let providerId = $derived(path.startsWith(base + "/add/") ? decodeURIComponent(path.slice((base + "/add/").length)) : "");
  let selected = $derived(providers.find(p => p.id === providerId) ?? (!phone && adding && !providerId ? providers[0] : undefined));
  let flow = $derived(keyFlow ? "key" : selected?.flows?.[0]);
  let accountId = $derived(path.startsWith(base + "/account/") ? decodeURIComponent(path.slice((base + "/account/").length)) : "");
  let account = $derived(accounts.find(a => a.id === accountId));
  let accountProviders = $derived([...providers, ...accounts.filter(a => !providers.some(p => p.id === a.provider)).filter((a, i, all) => all.findIndex(b => b.provider === a.provider) === i).map(a => ({ id: a.provider, displayName: a.provider, flows: [a.flow], models: [], connectFlow: a.flow, connectMethod: a.flow === "key" ? "key" as const : "oauth" as const }))]);
  let accountProvider = $derived(accountProviders.find(p => p.id === account?.provider));
  let accountUsage = $derived(usage.find(u => u.accountId === accountId));
  let selectedReportsWindows = $derived(flow === "callback" && usage.some(u => accounts.some(a => a.provider === selected?.id && a.id === u.accountId) && u.windows?.some(w => w.kind === "five_hour") && u.windows?.some(w => w.kind === "weekly")));
  const groups = [{ flow: "callback", title: "Sign in with your plan" }, { flow: "device", title: "Sign in with a code" }, { flow: "key", title: "API key" }] as const;
  const providerPath = (id: string) => base + "/add/" + encodeURIComponent(id);
  const accountPath = (id: string) => base + "/account/" + encodeURIComponent(id);
  function reset() {
    generation++; clearTimeout(timer); attempt = undefined; connectState = "idle"; redirect = ""; secret = ""; error = ""; reconnectId = undefined; keyFlow = false; copied = false; opened = false;
  }
  function reload() {
    void call("accounts.list", {}).then(result => { providers = result.providers ?? []; accounts = result.accounts ?? []; loaded = true; failed = false; }, (e: unknown) => { if (e instanceof SignedOut) signedout(); else if (!loaded) failed = true; });
    void call("accounts.quota", {}).then(result => { usage = result.accounts ?? []; }, () => {});
    void call("room.list", {}).then(result => { rooms = result.rooms ?? []; }, () => {});
  }
  onMount(() => { reload(); window.addEventListener("focus", reload); return () => { reset(); window.removeEventListener("focus", reload); }; });
  $effect(() => { path; return () => { generation++; clearTimeout(timer); attempt = undefined; connectState = "idle"; redirect = ""; secret = ""; error = ""; keyFlow = false; }; });
  function watch(id: string, ticket: number) {
    timer = setTimeout(() => {
      now = Date.now();
      void call("accounts.connect_status", { attempt: id }).then(result => {
        if (ticket !== generation) return;
        if (result.status === "connected") { reset(); reload(); document.getElementById("accounts-back")?.click(); }
        else if (result.status === "pending") watch(id, ticket);
        else { connectState = result.status; error = result.reason === "wrong_account" ? "That sign-in belongs to a different account." : result.reason === "adoption_failed" ? "Dyna cannot reconnect this sign-in. Remove it and add it again." : ""; }
      }, () => { if (ticket === generation) { connectState = "failed"; error = "Could not reach the service. Try again."; } });
    }, 2000);
  }
  function remaining(at: string) {
    const seconds = Math.max(0, Math.ceil((Date.parse(at) - now) / 1000));
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
  }
  async function start() {
    if (!selected || busy) return;
    busy = true; error = ""; const ticket = ++generation;
    label = label.trim() || `${selected.displayName} account`;
    const popup = flow !== "key" ? window.open("", "_blank") : null;
    if (popup) popup.opener = null;
    if (flow === "key") {
      const key = secret; secret = "";
      await call("accounts.set_key", { provider: selected.id, label, key }).then(() => { if (ticket === generation) { reset(); reload(); document.getElementById("accounts-back")?.click(); } }, () => { if (ticket === generation) error = "Could not save the key. Try again."; });
    } else {
      const request = reconnectId ? call("accounts.reconnect", { accountId: reconnectId }) : call("accounts.connect", { provider: selected.id, label });
      await request.then(result => {
        if (ticket !== generation) { popup?.close(); return; }
        attempt = result; connectState = "pending"; now = Date.now(); watch(result.attempt, ticket);
        if (popup) { popup.location.href = result.link; opened = true; }
      }, (e: unknown) => { popup?.close(); if (ticket === generation) { connectState = "failed"; error = reconnectUnavailable(e) ? "Dyna cannot reconnect this sign-in. Remove it and add it again." : refused(e) ? "The provider did not accept the sign-in." : "Could not reach the service. Try again."; } });
    }
    busy = false;
  }
  async function finish() {
    if (!attempt || busy || !redirect.trim()) return;
    const ticket = generation, url = redirect; redirect = ""; busy = true;
    await call("accounts.connect_callback", { attempt: attempt.attempt, redirectUrl: url }).catch(() => { if (ticket === generation) error = "Could not finish sign-in. Try again."; });
    busy = false;
  }
  async function remove(a: Account) {
    busy = true;
    await call("accounts.remove", { accountId: a.id }).then(() => { reload(); document.getElementById("accounts-back")?.click(); }, () => { error = "The account was not removed. Try again."; });
    busy = false;
  }
  async function setRooms(a: Account, ids: string[]) {
    busy = true;
    await call("accounts.set_limits", { accountId: a.id, rooms: ids }).then(reload, () => { error = "Room limits could not be saved. Try again."; });
    busy = false;
  }
</script>

{#snippet action()}<a class="btn add-button" href={base + "/add"} onclick={(e) => { reset(); label = ""; navigate(e); }}>+ Add an account</a>{/snippet}
{#snippet back()}<a href={base} onclick={navigate}>Accounts</a>{/snippet}
<a id="accounts-back" href={base} onclick={navigate} hidden>Back to Accounts</a>
{#if adding || accountId}
  <header class="subbar">{#if phone}<a href={adding && selected ? base + "/add" : base} onclick={navigate} aria-label={adding && selected ? "Back to providers" : "Back to Accounts"}><Icon name="backChevron" size={20} /></a>{:else}{@render back()} <span>/</span>{/if} <strong>{adding ? (phone && selected ? selected.displayName : "Add an account") : account?.label || "Account"}</strong></header>
{:else}{@render bar("Accounts", loaded ? accounts.length : undefined, action)}{/if}
<div class="body">
{#if failed}<p>Accounts did not load. <button class="btn" type="button" onclick={reload}>Try again</button></p>
{:else if !loaded}<p class="muted">Loading accounts…</p>
{:else if adding}
  <a id="picker-back" href={base + "/add"} onclick={navigate} hidden>Back to providers</a><div class="add-layout">
    {#if !phone || !selected}
      <section class="picker">
        <div class="search"><Icon name="search" size={18} /><input aria-label="Search providers" placeholder={`Search ${providers.length} providers`} bind:value={query} /></div>
        {#each groups as group}
          {@const matches = providerGroup(providers, group.flow, query, expanded)}
          {#if matches.count}<h3>{group.title} · {matches.count}</h3>
            {#each matches.items as provider (provider.id)}
              {@const connected = accounts.filter(a => a.provider === provider.id).length}
              <a class:chosen={selected?.id === provider.id} href={providerPath(provider.id)} onclick={(e) => { reset(); label = ""; navigate(e); }}><span class="initial">{provider.displayName.slice(0, 1)}</span><strong>{provider.displayName}</strong>{#if provider.flows?.length && provider.flows[0] !== "key" && provider.flows.includes("key")}<span class="muted">or API key</span>{:else if connected}<span class="muted">{connected} connected</span>{/if}</a>
            {/each}
            {#if matches.remaining}<button class="btn ghost" type="button" onclick={() => expanded = true}>Show {matches.remaining} more</button>{/if}
          {/if}
        {/each}
      </section>
    {/if}
    {#if selected}
      <div class="details">
        <section class="card connect">
          <h2><span class="initial">{selected.displayName.slice(0, 1)}</span>{selected.displayName}{#if !attempt}<span class="muted">{accounts.filter(a => a.provider === selected.id).length} connected</span>{:else if !phone && attempt.flow === "callback"}<span class="muted">Sign in with your plan · Dyna sees your plan limits, not your chats</span>{/if}</h2>
          {#if !attempt}
            <p><strong>{flow === "callback" ? "Sign in with your plan." : flow === "device" ? "Sign in with a code." : "API key."}</strong> {flow === "callback" ? `Use a ${selected.displayName} plan you already pay for. Dyna shows its usage limits when available.` : flow === "device" ? "Dyna shows a short code here. You enter it on the provider’s page, and Dyna finishes by itself." : "You paste a key once. It stays on this computer, and Dyna never shows it again."}</p>
            <p class="muted">Dyna keeps the sign-in on this computer and sends it only to {selected.displayName}.{#if flow === "callback"}{" "}It sees your plan limits, not your chats.{/if}</p>
            <label class="label-field">Label <input bind:value={label} placeholder="Work, Personal, …" disabled={busy} /></label>
            {#if flow === "key"}<label>{selected.keyName || "API key"} <input type="password" bind:value={secret} autocomplete="off" disabled={busy} /></label>{/if}
            <div class="start-actions"><button class="btn fill" type="button" disabled={busy || (flow === "key" && !secret.trim())} onclick={() => void start()}>{flow === "callback" ? `Sign in with ${selected.displayName}` : flow === "device" ? "Get a code" : "Save key"}</button>{#if !reconnectId && flow !== "key" && selected.flows?.includes("key")}<button class="btn ghost" type="button" onclick={() => keyFlow = true}>Use an API key instead</button>{/if}{#if flow === "key"}<a class="btn" href={base + "/add"} onclick={navigate}>Cancel</a>{/if}</div>
          {:else}
            <label class="label-field">Label <input value={label} disabled /></label>
            {#if attempt.flow === "device"}
              <p>Open the provider's page and enter this code.</p><div class="code"><strong>{attempt.code}</strong><button class="btn ghost" type="button" onclick={() => { void navigator.clipboard.writeText(attempt?.code ?? ""); copied = true; }}>{copied ? "Copied" : "Copy"}</button></div>
              <a href={attempt.link} target="_blank" rel="noopener noreferrer">Open sign-in ↗</a><p class="muted">Waiting for you to approve · Expires in {remaining(attempt.expiresAt)}</p>
            {:else}
              <div class="step-row"><span class="step-badge" class:checked={opened} aria-hidden="true">{opened ? "✓" : "1"}</span><p>Open the {selected.displayName} sign-in and approve Dyna.<br /><a href={attempt.link} target="_blank" rel="noopener noreferrer" onclick={() => opened = true}>{opened ? "Opened in your browser" : "Open sign-in"} ↗</a> <button class="btn ghost" type="button" onclick={() => { void navigator.clipboard.writeText(attempt?.link ?? ""); copied = true; }}>{copied ? "Copied" : "Copy link"}</button></p></div>
              <form onsubmit={(e) => { e.preventDefault(); void finish(); }}>
                <div class="step-row"><span class="step-badge" aria-hidden="true">2</span><label for="callback-address">Paste the address of the page you land on. That page may say it cannot be reached; that is expected.</label></div>
                <div class="callback-entry"><input id="callback-address" aria-label="Address of the page you land on" bind:value={redirect} placeholder="http://localhost/…" autocomplete="off" />
                  <div class="actions"><button class="btn fill" type="submit" disabled={busy || !redirect.trim()}>Finish</button><a class="btn" href={base + "/add"} onclick={navigate}>Cancel</a>{#if phone}<span class="muted">Leaving this page cancels.</span>{/if}</div></div>
              </form>
              <p class="muted">{#if !phone}Enter finishes. Escape, or leaving this page, cancels.{" "}{/if}The address holds a one-time code. Dyna hands it to the sign-in service and never keeps it.{#if attempt.expiresAt}{" "}Expires in {remaining(attempt.expiresAt)}.{/if}</p>
            {/if}
            {#if attempt.flow === "device"}<a class="btn" href={base + "/add"} onclick={navigate}>Cancel</a>{/if}
          {/if}
          {#if connectState === "expired"}<p>The sign-in expired. Cancel and try again.</p>{/if}
          {#if error}<p role="alert">{error}</p>{/if}
        </section>
        <section class="card legend">
          <h2>{attempt ? "After you finish" : "Three ways to connect"}</h2>
          {#if attempt}<p class="muted after"><strong>{label}</strong>{" "}joins {selected.displayName} in your account list{#if selectedReportsWindows}, with its 5-hour and Weekly limits{/if}.</p><p class="muted after">It serves every room until you choose rooms in its view.</p>
          {:else}<p><strong>Sign in with your plan</strong> <span class="muted">You approve Dyna on the provider's page, then paste back the address it lands on. Dyna shows your plan's limits.</span></p>
          <p><strong>Sign in with a code</strong> <span class="muted">Dyna shows a short code here. You enter it on the provider's page, and Dyna finishes by itself.</span></p>
          <p><strong>API key</strong> <span class="muted">You paste a key once. It stays on this computer, and Dyna never shows it again.</span></p>{/if}
        </section>
      </div>
    {/if}
  </div>
{:else if accountId}
  {#if account && accountProvider}
    <section class="account-view">
      <h1><span class="initial">{accountProvider.displayName.slice(0, 1)}</span>{account.label}</h1>
      <p class="muted">{accountProvider.displayName}{#if account.flow === "key"} · API key{/if}{#if accountUsage?.observedAt}{" · "}usage at {new Date(accountUsage.observedAt).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}{/if}</p>
      <h2>Usage</h2>
      {#if accountUsage && visibleWindows(accountUsage.windows ?? []).length}<div class="meters">{#each visibleWindows(accountUsage.windows ?? []) as window (window.name)}<AccountMeter {window} now={Date.now()} />{/each}</div>{:else}<p class="muted">No usage limits reported.</p>{/if}
      <h2>Rooms</h2>
      <p><label><input type="radio" name="room-scope" checked={!account.rooms?.length} disabled={busy} onchange={() => void setRooms(account, [])} /> Every room</label> <label><input type="radio" name="room-scope" checked={!!account.rooms?.length} disabled={busy || !rooms.length} onchange={() => void setRooms(account, [rooms[0].id])} /> Only these rooms</label></p>
      {#if account.rooms?.length}{#each rooms as room (room.id)}<label><input type="checkbox" checked={account.rooms.includes(room.id)} disabled={busy || (account.rooms.length === 1 && account.rooms.includes(room.id))} onchange={(e) => void setRooms(account, e.currentTarget.checked ? [...(account.rooms ?? []), room.id] : (account.rooms ?? []).filter(id => id !== room.id))} /> {room.name}</label>{/each}{/if}
      <h2>Sign-in</h2>
      {#if account.flow !== "key" && providers.some(p => p.id === account.provider)}<div class="start-actions"><a class="btn" href={providerPath(accountProvider.id)} onclick={(e) => { navigate(e); label = account.label; reconnectId = account.id; }}>Sign in again</a> <span class="muted">Use it if the account changed plans or Dyna asks.</span></div>{:else if account.flow === "key"}<p class="muted">This account uses an API key.</p>{/if}
      <div class="remove"><div><strong>Remove {account.label}</strong><p class="muted">Dyna deletes this {account.flow === "key" ? "key" : "sign-in"} from this computer. {#if accounts.some(a => a.provider === account.provider && a.id !== account.id)}Sessions on it move to another {accountProvider.displayName} account at their next model call.{:else}You can connect it again later.{/if}</p></div><button class="btn danger" type="button" disabled={busy} onclick={() => void remove(account)}>Remove</button></div>
      {#if error}<p role="alert">{error}</p>{/if}
    </section>
  {:else}<p>Account not found. {@render back()}</p>{/if}
{:else}
  <p class="muted">{#if accounts.length}{accounts.length} {accounts.length === 1 ? "account" : "accounts"} with {new Set(accounts.map(a => a.provider)).size} providers{#if accounts.some(a => a.state === "sign_in_expired")}{" · "}<StatusPill tone="error" label={`${accounts.filter(a => a.state === "sign_in_expired").length} sign-in expired`} />{/if}{:else}Accounts you connect will appear here.{/if}</p>
  {#each accountProviders.filter(p => accounts.some(a => a.provider === p.id)) as provider (provider.id)}
    {@const group = accountRows(accounts.filter(a => a.provider === provider.id))}
    <section><div class="heading"><h2><span class="initial">{provider.displayName.slice(0, 1)}</span>{provider.displayName}</h2><span class="muted">{group.length}</span>{#if providers.some(p => p.id === provider.id)}<a class="btn ghost" href={providerPath(provider.id)} onclick={(e) => { reset(); label = ""; navigate(e); }}>{provider.flows?.[0] === "key" ? "+ Add a key" : "+ Connect another"}</a>{/if}</div>
      <div class="list">{#each group as item, index (item.id)}
        {@const status = accountState(item)}
        {@const rowUsage = usage.find(u => u.accountId === item.id)}
        <div class="account-row"><a class="row" href={accountPath(item.id)} onclick={navigate}><div><strong>{item.label || `${provider.displayName} account ${index + 1}`}</strong>{#if status.pill}{" "}<StatusPill tone={status.pill.tone} label={status.pill.label} />{/if}<div class="muted">{#if item.flow === "key"}API key · {/if}{item.rooms?.length ? `${item.rooms.length} rooms` : "Every room"}{#if status.line}{" · "}{status.line}{/if}{#if rowUsage?.observedAt}{" · "}usage at {new Date(rowUsage.observedAt).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}{/if}</div></div>{#if rowUsage && !rowUsage.cause && visibleWindows(rowUsage.windows ?? []).length}<div class="row-meters">{#each visibleWindows(rowUsage.windows ?? []) as window (window.name)}<AccountMeter {window} now={Date.now()} />{/each}</div>{:else if item.state !== "sign_in_expired"}<span class="muted no-usage">No usage data for this provider yet.</span>{/if}</a>{#if item.state === "sign_in_expired" && item.flow !== "key" && providers.some(p => p.id === provider.id)}<div class="retry"><a class="btn fill" href={providerPath(provider.id)} onclick={(e) => { navigate(e); label = item.label; reconnectId = item.id; }}>Sign in again</a><span class="muted">Its limits show once it is signed in.</span></div>{/if}<a class="chevron" href={accountPath(item.id)} onclick={navigate} aria-label={`View ${item.label}`}>›</a></div>
      {/each}</div>
    </section>
  {/each}
{/if}
</div>

<svelte:window onkeydown={(e) => { if (e.key === "Escape" && attempt) { reset(); document.getElementById("picker-back")?.click(); } }} />
<style>
  .subbar { height: 52px; flex: none; display: flex; align-items: center; gap: 8px; padding: 0 24px; border-bottom: 1px solid var(--line); }
  .subbar a, .subbar span { color: var(--muted); } .subbar a { text-decoration: none; display: inline-flex; align-items: center; }
  .body { flex: 1; min-height: 0; overflow: auto; box-sizing: border-box; padding: 20px 24px; display: grid; align-content: start; gap: 18px; }
  p { margin: 0; line-height: 1.45; } .muted { color: var(--muted); font-size: 12.5px; }
  h1, h2, h3 { margin: 0; font-weight: 600; } h1 { font-size: 20px; display: flex; align-items: center; gap: 12px; } h2 { font-size: 16px; } h3 { font-size: 13px; color: var(--muted); margin: 16px 8px 4px; }
  a { color: var(--focus); } .btn { text-decoration: none; display: inline-flex; align-items: center; justify-content: center; } .add-button { margin-left: auto; }
  .initial { display: inline-grid; place-items: center; flex: none; width: 28px; height: 28px; border-radius: 6px; background: var(--selected); color: var(--muted); font-size: 12px; }
  .heading { display: flex; align-items: center; gap: 8px; margin: 0 8px 8px; } .heading h2, .connect h2 { display: flex; align-items: center; gap: 8px; } .heading .btn { margin-left: auto; }
  .list, .card, .picker { border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .account-row { display: flex; align-items: center; } .account-row + .account-row { border-top: 1px solid var(--line); } .row { flex: 1; min-width: 0; min-height: 58px; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 14px; color: var(--text); text-decoration: none; } .row:hover, .picker a:hover { background: var(--hover); } .row .muted { margin-top: 4px; } .row-meters { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 20px; width: min(65%, 700px); margin-left: auto; }
  .add-layout { display: grid; grid-template-columns: minmax(240px, 300px) minmax(0, 1fr); gap: 20px; align-items: start; } .picker { padding: 8px; } .search { display: flex; align-items: center; gap: 8px; padding-left: 10px; border: 1px solid var(--line); border-radius: 6px; background: var(--bg); color: var(--muted); } .picker .search input { border: 0; } .picker a { display: flex; align-items: center; gap: 10px; color: var(--text); text-decoration: none; padding: 8px; border-radius: 6px; } .picker a .muted { margin-left: auto; } .picker a.chosen { background: var(--selected); }
  .details { display: grid; gap: 16px; } .card { padding: 20px; } .connect { display: grid; justify-items: start; gap: 16px; } .connect h2 .muted { font-weight: 400; } .connect label, form { display: grid; gap: 8px; width: 100%; } .connect .label-field { grid-template-columns: 64px minmax(0, 320px); align-items: center; color: var(--muted); } .connect input, .picker input { box-sizing: border-box; width: 100%; padding: 10px; border: 1px solid var(--line); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; } .step-row { display: flex; align-items: flex-start; gap: 10px; width: 100%; } .step-badge { display: inline-grid; place-items: center; flex: none; width: 22px; height: 22px; border-radius: 50%; background: var(--selected); color: var(--muted); font-size: 12px; } .step-badge.checked { background: transparent; border: 1px solid var(--line); box-sizing: border-box; } .callback-entry { display: flex; gap: 8px; width: 100%; } .callback-entry input { flex: 1; min-width: 0; } .actions, .start-actions { display: flex; gap: 8px; align-items: center; } .retry { display: flex; align-items: center; gap: 12px; padding: 8px; text-decoration: none; } .retry .btn { text-decoration: none; } .chevron { padding: 12px 14px; color: var(--muted); text-decoration: none; } .no-usage { margin-left: auto; } .btn:not(.fill):not(.danger) { color: var(--text); } .code { display: flex; gap: 12px; align-items: center; font-family: var(--mono); font-size: 20px; }
  .legend { display: grid; gap: 16px; } .legend .after strong { color: var(--text); } .legend p:not(.after) { display: grid; grid-template-columns: 180px 1fr; gap: 16px; } .account-view { display: grid; justify-items: start; gap: 12px; max-width: 900px; } .account-view h2 { margin-top: 28px; } .meters { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 24px; width: 100%; } .remove { border-top: 1px solid var(--line); margin-top: 24px; padding-top: 20px; width: 100%; display: flex; justify-content: space-between; gap: 16px; }
  @media (max-width: 640px) { .body { padding: 12px 16px; } .subbar { padding: 0 16px; } .add-layout { display: block; } .picker { border: 0; padding: 0; } .search { position: sticky; top: 0; z-index: 1; } .connect .label-field { grid-template-columns: 1fr; } .legend { display: none; } .meters { grid-template-columns: 1fr; } .account-row, .row { flex-wrap: wrap; } .account-view .start-actions { flex-direction: column; align-items: flex-start; } .remove { flex-direction: column; } .remove .btn { width: 100%; } .retry { order: 2; flex-basis: 100%; } .chevron { order: 1; } .row-meters { order: 3; width: 100%; grid-template-columns: 1fr; } .callback-entry { display: block; } .actions { position: fixed; z-index: 2; bottom: 0; left: 0; right: 0; padding: 12px 16px; border-top: 1px solid var(--line); background: var(--bg); flex-wrap: wrap; } .actions .btn { flex: 1; min-height: 44px; } .actions .muted { width: 100%; text-align: center; } }
</style>

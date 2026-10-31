<script lang="ts">
  import { onMount, type Snippet } from "svelte";
  import { call, SignedOut } from "../client.ts";
  import type { Account, AccountProviderInfo, AccountsConnectResponse } from "../contract.gen.ts";
  import StatusPill from "./StatusPill.svelte";

  let { bar, phone, signedout }: { bar: Snippet<[string, number | undefined, Snippet | undefined]>; phone: boolean; signedout: () => void } = $props();
  let providers: AccountProviderInfo[] = $state([]), accounts: Account[] = $state([]);
  let loaded = $state(false), loadFailed = $state(false);
  let adding = $state(false), selected: AccountProviderInfo | undefined = $state(), inline = $state(false);
  let label = $state(""), secret = $state(""), redirect = $state("");
  let attempt: AccountsConnectResponse | undefined = $state();
  let connectState: "idle" | "pending" | "failed" | "expired" = $state("idle");
  let busy = $state(false), keyFailed = $state(false), now = $state(Date.now());
  let timer: ReturnType<typeof setTimeout> | undefined;
  let generation = 0;

  function reload() {
    void call("accounts.list", {}).then(result => {
      providers = result.providers ?? []; accounts = result.accounts ?? []; loaded = true; loadFailed = false;
    }, (error: unknown) => { if (error instanceof SignedOut) signedout(); else loadFailed = true; });
  }
  onMount(() => { reload(); return () => { generation++; clearTimeout(timer); }; });
  function close() {
    generation++; clearTimeout(timer); adding = false; selected = undefined; attempt = undefined;
    label = ""; secret = ""; redirect = ""; connectState = "idle"; keyFailed = false;
  }
  function choose(provider: AccountProviderInfo, inPlace: boolean) {
    close(); adding = true; selected = provider; inline = inPlace;
  }
  function watch(id: string, ticket: number) {
    timer = setTimeout(() => {
      now = Date.now();
      void call("accounts.connect_status", { attempt: id }).then(result => {
        if (ticket !== generation) return;
        if (result.status === "connected") { close(); reload(); }
        else if (result.status === "pending") watch(id, ticket);
        else connectState = result.status;
      }, () => { if (ticket === generation) connectState = "failed"; });
    }, 2000);
  }
  async function start() {
    if (!selected || busy) return;
    busy = true; connectState = "idle"; keyFailed = false;
    const ticket = ++generation;
    if (selected.connectMethod === "key") {
      const key = secret; secret = "";
      await call("accounts.set_key", { provider: selected.id, label, key }).then(() => { if (ticket === generation) { close(); reload(); } }, () => { if (ticket === generation) keyFailed = true; });
    } else {
      await call("accounts.connect", { provider: selected.id, label }).then(result => {
        if (ticket !== generation) return;
        attempt = result; connectState = "pending"; now = Date.now(); watch(result.attempt, ticket);
      }, () => { if (ticket === generation) connectState = "failed"; });
    }
    busy = false;
  }
  async function finish() {
    if (!attempt || busy) return;
    const url = redirect, ticket = generation; redirect = ""; busy = true;
    await call("accounts.connect_callback", { attempt: attempt.attempt, redirectUrl: url }).catch(() => { if (ticket === generation) connectState = "failed"; });
    busy = false;
  }
  const remaining = (at: string, time: number) => {
    const seconds = Math.max(0, Math.ceil((Date.parse(at) - time) / 1000));
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
  };
</script>

{#snippet action()}<button class="btn" type="button" onclick={() => { close(); adding = true; }}>+ Add an account</button>{/snippet}
{#snippet form()}
  {#if selected}
    <div class="connect">
      <h3>New {selected.displayName} account</h3>
      <label>Label <input bind:value={label} placeholder="A name for this account" disabled={busy || !!attempt} /></label>
      {#if selected.connectMethod === "key"}
        <label>Key <input type="password" bind:value={secret} autocomplete="off" disabled={busy} /></label>
        {#if keyFailed}<p class="error" role="alert">{selected.displayName} refused this key. Check it and paste it again.</p>{/if}
        <button class="btn fill" type="button" disabled={busy || !secret} onclick={() => void start()}>Add key</button>
      {:else if connectState === "expired"}
        <p>The code expired.</p><button class="btn fill" type="button" disabled={busy} onclick={() => { attempt = undefined; void start(); }}>Get a new code</button>
      {:else if connectState === "failed"}
        <p class="error" role="alert">{selected.displayName} did not accept the sign-in.</p><button class="btn fill" type="button" disabled={busy} onclick={() => { attempt = undefined; void start(); }}>Try again</button>
      {:else if !attempt}
        <p>Connect this account to let Dyna use it.</p><button class="btn fill" type="button" disabled={busy} onclick={() => void start()}>Start sign-in</button>
      {:else if attempt.flow === "device"}
        <p>Open the sign-in and enter this code.</p><strong class="code">{attempt.code}</strong>
        <p><a href={attempt.link} target="_blank" rel="noopener noreferrer">Open {selected.displayName} sign-in ↗</a></p>
        <p class="muted">Waiting for you to approve · Expires in {remaining(attempt.expiresAt, now)}</p>
      {:else}
        <p>1　<a href={attempt.link} target="_blank" rel="noopener noreferrer">Open the {selected.displayName} sign-in and approve Dyna ↗</a></p>
        <p>2　Copy the address of the page you land on and paste it here. That page may say it cannot be reached; that is expected.</p>
        <div class="finish"><input aria-label="Address of the page you land on" bind:value={redirect} placeholder="http://localhost/..." autocomplete="off" /><button class="btn fill" type="button" disabled={busy || !redirect} onclick={() => void finish()}>Finish</button></div>
        <p class="muted">The address holds a one-time code. Dyna hands it to the sign-in service and never keeps it. Expires in {remaining(attempt.expiresAt, now)}.</p>
      {/if}
    </div>
  {/if}
{/snippet}
{#if phone && adding && !inline}<header class="phone-bar"><button class="btn ghost" type="button" onclick={close} aria-label="Back to Accounts">‹</button><strong>Add an account</strong></header>
{:else}{@render bar("Accounts", loaded ? accounts.length : undefined, action)}{/if}
<div class="body">
  {#if loadFailed}<p>Accounts did not load. <button class="btn" type="button" onclick={reload}>Try again</button></p>
  {:else if !loaded}<p class="muted">Loading accounts…</p>
  {:else}
    {#if adding && !inline}
      <section class="add">
        <div class="heading"><h2>Add an account</h2><button class="btn ghost" type="button" onclick={close}>Cancel</button></div>
        <p class="muted">Choose a provider to connect.</p>
        <div class="providers">{#each providers as provider (provider.id)}
          <button type="button" class:chosen={selected?.id === provider.id} onclick={() => choose(provider, false)}>{provider.displayName} <span>{provider.connectMethod === "key" ? "Key" : "Sign-in"}</span></button>
        {/each}</div>
        {@render form()}
      </section>
    {/if}
    {#if !phone || !adding}
      <p class="muted">{accounts.length} {accounts.length === 1 ? "account" : "accounts"} with {new Set(accounts.map(a => a.provider)).size} providers</p>
      {#each providers.filter(p => accounts.some(a => a.provider === p.id)) as provider (provider.id)}
        {@const group = accounts.filter(a => a.provider === provider.id)}
        <section>
          <div class="heading"><h2><span class="initial">{provider.displayName.slice(0, 1)}</span>{provider.displayName}</h2>
            <span class="muted">{group.length} · {provider.connectMethod === "key" ? "API key" : "Sign-in"}</span>
            <button class="btn ghost connect-button" type="button" onclick={() => inline && selected?.id === provider.id ? close() : choose(provider, true)}>{inline && selected?.id === provider.id ? "Cancel" : provider.connectMethod === "key" ? "+ Add a key" : "+ Connect another"}</button>
          </div>
          <div class="list">
            {#each group as account, index (account.id)}
              <div class="row"><div><strong>{account.label || `${provider.displayName} account ${index + 1}`}</strong>
                {#if account.state === "sign_in_expired"} <StatusPill tone="error" label="Sign-in expired" />
                {:else if account.state === "quota_low"} <StatusPill tone="warning" label="Low usage" />{/if}
                <div class="muted">{account.rooms?.length ? `${account.rooms.length} rooms` : "Every room"}</div></div>
              </div>
            {/each}
            {#if inline && selected?.id === provider.id}{@render form()}{/if}
          </div>
        </section>
      {/each}
    {/if}
  {/if}
</div>

<style>
  .phone-bar { height: 52px; flex: none; display: flex; align-items: center; gap: 8px; padding: 0 16px; border-bottom: 1px solid var(--line); }
  .body { flex: 1; min-height: 0; overflow: auto; padding: 20px 24px; display: grid; align-content: start; gap: 18px; }
  p { margin: 0; } .muted { color: var(--muted); font-size: 12.5px; }
  .heading { display: flex; align-items: center; gap: 8px; margin: 0 8px 8px; }
  h2, h3 { margin: 0; font-size: 14px; font-weight: 600; } h2 { display: flex; align-items: center; gap: 8px; }
  .initial { display: grid; place-items: center; width: 24px; height: 24px; border-radius: 6px; background: var(--selected); color: var(--muted); font-size: 12px; }
  .connect-button, .heading > .ghost { margin-left: auto; }
  .list, .add { border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .row { min-height: 58px; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 14px; }
  .row + .row, .connect { border-top: 1px solid var(--line); }
  .row strong { font-size: 14px; } .row .muted { margin-top: 3px; }
  .add { padding: 16px; display: grid; gap: 12px; } .add .heading { margin: 0; }
  .providers { display: flex; flex-wrap: wrap; gap: 8px; }
  .providers button { border: 1px solid var(--line); background: transparent; color: var(--text); padding: 8px 10px; border-radius: 6px; font: inherit; cursor: pointer; }
  .providers button.chosen { border-color: var(--focus); background: var(--selected); }
  .providers span { color: var(--muted); font-size: 12px; }
  .connect { display: grid; justify-items: start; gap: 12px; padding: 16px 48px; font-size: 14px; }
  .connect label { display: grid; grid-template-columns: 70px minmax(0, 450px); align-items: center; gap: 12px; width: 100%; color: var(--muted); }
  input { min-width: 0; width: 100%; box-sizing: border-box; padding: 8px 10px; border: 1px solid var(--line); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; }
  .finish { display: flex; gap: 8px; width: min(720px, 100%); } a { color: var(--focus); } .error { color: var(--danger); } .code { font-size: 24px; font-variant-numeric: tabular-nums; }
  @media (max-width: 640px) { .body { padding: 12px 16px; } .connect { padding: 16px; } .connect label { grid-template-columns: 1fr; gap: 4px; } .heading { flex-wrap: wrap; } .row { min-height: 44px; } }
</style>

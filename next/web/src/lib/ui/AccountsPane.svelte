<script lang="ts">
  import { onMount, type Snippet } from "svelte";
  import { call, SignedOut } from "../client.ts";
  import { accountRows, accountState, refused, removalPrompt } from "../accounts.ts";
  import type { Account, AccountProviderInfo, AccountsConnectResponse } from "../contract.gen.ts";
  import StatusPill from "./StatusPill.svelte";
  import Icon from "./Icon.svelte";

  let { bar, phone, signedout }: { bar: Snippet<[string, number | undefined, Snippet | undefined]>; phone: boolean; signedout: () => void } = $props();
  let providers: AccountProviderInfo[] = $state([]), accounts: Account[] = $state([]);
  let loaded = $state(false), loadFailed = $state(false);
  let adding = $state(false), selected: AccountProviderInfo | undefined = $state(), inline = $state(false);
  let label = $state(""), secret = $state(""), redirect = $state(""), linkText = $state("");
  let attempt: AccountsConnectResponse | undefined = $state();
  let connectState: "idle" | "pending" | "failed" | "expired" = $state("idle");
  let busy = $state(false), opened = $state(false), now = $state(Date.now());
  let removing: string | undefined = $state(), removeFailed = $state(false), unreachable = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let generation = 0, loadGeneration = 0;

  function reload() {
    const ticket = ++loadGeneration;
    void call("accounts.list", {}).then(result => {
      if (ticket !== loadGeneration) return;
      providers = result.providers ?? []; accounts = result.accounts ?? []; loaded = true; loadFailed = false;
    }, (error: unknown) => { if (ticket !== loadGeneration) return; if (error instanceof SignedOut) signedout(); else if (!loaded) loadFailed = true; });
  }
  onMount(() => {
    reload();
    window.addEventListener("focus", reload);
    return () => { generation++; loadGeneration++; clearTimeout(timer); window.removeEventListener("focus", reload); };
  });
  function close() {
    generation++; clearTimeout(timer); adding = false; inline = false; selected = undefined; attempt = undefined;
    label = ""; secret = ""; redirect = ""; connectState = "idle"; unreachable = false; opened = false;
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
      }, (error: unknown) => { if (ticket === generation) { connectState = "failed"; unreachable = !refused(error); } });
    }, 2000);
  }
  async function start() {
    if (!selected || busy) return;
    busy = true; connectState = "idle"; unreachable = false;
    const ticket = ++generation;
    if (selected.connectMethod === "key") {
      const key = secret; secret = "";
      await call("accounts.set_key", { provider: selected.id, label, key }).then(() => { if (ticket === generation) { close(); reload(); } }, () => { if (ticket === generation) { unreachable = true; reload(); } });
    } else {
      await call("accounts.connect", { provider: selected.id, label }).then(result => {
        if (ticket !== generation) return;
        attempt = result;
        const url = URL.canParse(result.link) ? new URL(result.link) : undefined;
        linkText = url ? url.host + url.pathname : result.link;
        connectState = "pending"; now = Date.now(); watch(result.attempt, ticket);
      }, (error: unknown) => { if (ticket === generation) { connectState = "failed"; unreachable = !refused(error); } });
    }
    busy = false;
  }
  async function finish() {
    if (!attempt || busy) return;
    const url = redirect, ticket = generation; redirect = ""; busy = true;
    await call("accounts.connect_callback", { attempt: attempt.attempt, redirectUrl: url }).catch((error: unknown) => { if (ticket === generation) { connectState = "failed"; unreachable = !refused(error); } });
    busy = false;
  }
  async function remove(account: Account) {
    if (busy) return;
    busy = true; removeFailed = false;
    await call("accounts.remove", { accountId: account.id }).then(() => { removing = undefined; reload(); }, () => { removeFailed = true; });
    busy = false;
  }
  const remaining = (at: string, time: number) => {
    const seconds = Math.max(0, Math.ceil((Date.parse(at) - time) / 1000));
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
  };
</script>

{#snippet action()}<button class="btn add-button" type="button" onclick={() => { close(); adding = true; }}>+ Add an account</button>{/snippet}
{#snippet form()}
  {#if selected}
    <div class="connect">
      <h3>New {selected.displayName} account</h3>
      {#if selected.connectMethod === "key"}
        <div class="key-fields"><label>Label <input bind:value={label} placeholder="A name for this account" disabled={busy} /></label>
        <label>Key <input type="password" bind:value={secret} autocomplete="off" disabled={busy} class:invalid={unreachable} /></label>
        <button class="btn fill" type="button" disabled={busy || !secret || !label.trim()} onclick={() => void start()}>Save</button></div>
        <p class="muted">Dyna hands the key to the sign-in service and never shows it again.</p>
        {#if unreachable}<p class="error" role="alert">Could not reach the service. Try again.</p>{/if}
      {:else}
        <label>Label <input bind:value={label} placeholder="A name for this account" disabled={busy || !!attempt} /></label>
        {#if connectState === "expired"}
        <p>The code expired.</p><button class="btn fill" type="button" disabled={busy} onclick={() => { attempt = undefined; void start(); }}>Get a new code</button>
      {:else if connectState === "failed"}
        <p class="error" role="alert">{unreachable ? "Could not reach the service. Try again." : `${selected.displayName} did not accept the sign-in.`}</p><button class="btn fill" type="button" disabled={busy} onclick={() => { attempt = undefined; void start(); }}>Try again</button>
      {:else if !attempt}
        <p>Connect this account to let Dyna use it.</p><button class="btn fill" type="button" disabled={busy || !label.trim()} onclick={() => void start()}>Start sign-in</button>
      {:else if attempt.flow === "device"}
        <p><span class="step">1</span> Open <a href={attempt.link} target="_blank" rel="noopener noreferrer">{linkText} ↗</a> and enter this code:</p><strong class="code">{attempt.code}</strong>
        <p class="muted">Waiting for you to approve · Expires in {remaining(attempt.expiresAt, now)}</p>
      {:else}
        <p><span class:done={opened} class="step">{opened ? "✓" : "1"}</span> Open the {selected.displayName} sign-in and approve Dyna. <a href={attempt.link} target="_blank" rel="noopener noreferrer" onclick={() => { opened = true; }}>{opened ? "Opened ↗" : "Open sign-in ↗"}</a></p>
        <p><span class="step">2</span> Copy the address of the page you land on and paste it here. That page may say it cannot be reached; that is expected.</p>
        <div class="finish"><input aria-label="Address of the page you land on" bind:value={redirect} placeholder="http://localhost/..." autocomplete="off" /><button class="btn fill" type="button" disabled={busy || !redirect} onclick={() => void finish()}>Finish</button></div>
        <p class="muted">The address holds a one-time code. Dyna hands it to the sign-in service and never keeps it. Expires in {remaining(attempt.expiresAt, now)}.</p>
        {/if}
      {/if}
    </div>
  {/if}
{/snippet}
{#if phone && adding && !inline}<header class="phone-bar"><button class="btn ghost" type="button" onclick={close} aria-label="Back to Accounts"><Icon name="backChevron" size={20} /></button><strong>Add an account</strong></header>
{:else}{@render bar("Accounts", loaded ? accounts.length : undefined, action)}{/if}
<div class="body">
  {#if loadFailed}<p>Accounts did not load. <button class="btn" type="button" onclick={reload}>Try again</button></p>
  {:else if !loaded}<p class="muted">Loading accounts…</p>
  {:else}
    {#if adding && !inline}
      <section class="add">
        {#if !phone}<div class="heading"><h2>Add an account</h2><button class="btn ghost" type="button" onclick={close}>Cancel</button></div>{/if}
        <p class="muted">Pick a provider. Each one connects in its own way.</p>
        <div class="providers">{#each providers as provider (provider.id)}
          <button type="button" class:chosen={selected?.id === provider.id} onclick={() => choose(provider, false)}><span class="initial">{provider.displayName.slice(0, 1)}</span>{provider.displayName} <span>{provider.connectFlow === "key" ? "key" : provider.connectFlow === "device" ? "code" : "sign-in"}</span></button>
        {/each}</div>
        {@render form()}
      </section>
    {/if}
    {#if !phone || !adding || inline}
      <p class="muted">{accounts.length} {accounts.length === 1 ? "account" : "accounts"} with {new Set(accounts.map(a => a.provider)).size} providers{#if accounts.some(a => a.state === "sign_in_expired")}{" · "}<StatusPill tone="error" label={`${accounts.filter(a => a.state === "sign_in_expired").length} sign-in expired`} />{/if}</p>
      {#each providers.filter(p => accounts.some(a => a.provider === p.id)) as provider (provider.id)}
        {@const group = accountRows(accounts.filter(a => a.provider === provider.id))}
        <section>
          <div class="heading"><h2><span class="initial">{provider.displayName.slice(0, 1)}</span>{provider.displayName}</h2>
            <span class="muted">{group.length} · {provider.connectMethod === "key" ? "API key" : "Sign-in"}</span>
            <button class="btn ghost connect-button" type="button" onclick={() => inline && selected?.id === provider.id ? close() : choose(provider, true)}>{inline && selected?.id === provider.id ? "Cancel" : provider.connectMethod === "key" ? "+ Add a key" : "+ Connect another"}</button>
          </div>
          <div class="list">
            {#each group as account, index (account.id)}
              {@const status = accountState(account)}
              <div class="row"><div><strong>{account.label || `${provider.displayName} account ${index + 1}`}</strong>
                {#if status.pill} <StatusPill tone={status.pill.tone} label={status.pill.label} />{/if}
                <div class="muted">{account.rooms?.length ? `${account.rooms.length} rooms` : "Every room"}{#if status.line}{" · "}{status.line}{/if}</div></div>
                {#if account.state === "sign_in_expired"}
                  <div class="expired"><span>Its limits show once it is signed in.</span><span>Remove it, then use Connect another to sign in again.</span></div>
                {/if}
                  {#if removing === account.id}<div class="remove-confirm"><span>{removalPrompt(provider.connectMethod)}</span><button class="btn danger" type="button" disabled={busy} onclick={() => void remove(account)}>Remove</button><button class="btn ghost" type="button" disabled={busy} onclick={() => { removing = undefined; removeFailed = false; }}>Keep</button>{#if removeFailed}<span class="error" role="alert">The account was not removed. Try again.</span>{/if}</div>
                  {:else}<button class="btn ghost" type="button" onclick={() => { removing = account.id; removeFailed = false; }}>Remove</button>{/if}
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
  .add-button { margin-left: auto; }
  .phone-bar { height: 52px; flex: none; display: flex; align-items: center; gap: 8px; padding: 0 16px; border-bottom: 1px solid var(--line); }
  .body { flex: 1; min-height: 0; overflow: auto; width: min(1096px, 100%); box-sizing: border-box; padding: 20px 24px; display: grid; align-content: start; gap: 18px; }
  p { margin: 0; } .muted { color: var(--muted); font-size: 12.5px; }
  .heading { display: flex; align-items: center; gap: 8px; margin: 0 8px 8px; }
  h2, h3 { margin: 0; font-size: 14px; font-weight: 600; } h2 { display: flex; align-items: center; gap: 8px; }
  .initial { display: grid; place-items: center; width: 24px; height: 24px; border-radius: 6px; background: var(--selected); color: var(--muted); font-size: 12px; }
  .connect-button, .heading > .ghost { margin-left: auto; }
  .list, .add { border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .row { min-height: 58px; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 14px; }
  .row + .row, .connect { border-top: 1px solid var(--line); }
  .expired { display: grid; gap: 4px; margin-left: auto; color: var(--muted); font-size: 12.5px; }
  .remove-confirm { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; font-size: 13px; }
  .row strong { font-size: 14px; } .row .muted { margin-top: 3px; }
  .add { padding: 16px; display: grid; gap: 12px; } .add .heading { margin: 0; }
  .providers { display: flex; flex-wrap: wrap; gap: 8px; }
  .providers button { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; border: 1px solid var(--line); background: transparent; color: var(--text); padding: 8px 10px; border-radius: 6px; font: inherit; cursor: pointer; }
  .providers button.chosen { border-color: var(--focus); background: var(--selected); }
  .providers span { color: var(--muted); font-size: 12px; }
  .connect { display: grid; justify-items: start; gap: 12px; padding: 16px 48px; font-size: 14px; }
  .key-fields { display: flex; align-items: end; gap: 12px; width: 100%; }
  .connect .key-fields label { flex: 1; display: grid; grid-template-columns: 1fr; gap: 4px; }
  .connect label { display: grid; grid-template-columns: 70px minmax(0, 450px); align-items: center; gap: 12px; width: 100%; color: var(--muted); }
  input { min-width: 0; width: 100%; box-sizing: border-box; padding: 8px 10px; border: 1px solid var(--line); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; }
  input.invalid { border-color: var(--danger); }
  .step { display: inline-grid; place-items: center; width: 22px; height: 22px; margin-right: 8px; border-radius: 50%; background: var(--selected); color: var(--muted); font-size: 12px; }
  .step.done { background: transparent; border: 1px solid var(--line); }
  .connect a { text-decoration: none; font-weight: 500; }
  .finish { display: flex; gap: 8px; width: min(720px, 100%); } a { color: var(--focus); } .error { color: var(--danger); } .code { font-family: var(--mono); font-size: 24px; letter-spacing: 0.08em; font-variant-numeric: tabular-nums; }
  @media (min-width: 641px) { .row:has(.remove-confirm) { flex-wrap: wrap; } .remove-confirm { width: 100%; } .remove-confirm > span:first-child { margin-right: auto; } }
  @media (max-width: 640px) { .row { display: grid; grid-template-columns: minmax(0, 1fr) auto; } .row > .btn { grid-column: 2; grid-row: 1; } .expired, .remove-confirm { grid-column: 1 / -1; } .expired { grid-row: 2; margin: 0; } .expired + .remove-confirm { grid-row: 3; } .row > .btn, .connect-button { min-height: 40px; } .row > .btn { padding: 0 12px; border: 1px solid var(--line); } .body { padding: 12px 16px; } .connect { padding: 16px; } .key-fields { flex-direction: column; align-items: stretch; } .connect label { grid-template-columns: 1fr; gap: 4px; } .heading { flex-wrap: wrap; } .row { min-height: 44px; } }
</style>

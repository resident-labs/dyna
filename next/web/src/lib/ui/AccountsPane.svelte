<script lang="ts">
  import { onMount, tick, type Snippet } from "svelte";
  import { call, SignedOut } from "../client.ts";
  import { accountChips, accountItems, accountLine, accountPill, addProviderError, labelRefusal, parseModelIDs, providerChoices, refused, reconnectUnavailable, removalPrompt,
    usageHint, usageNote, type AccountItem } from "../accounts.ts";
  import { balancesText, visibleWindows } from "../usage.ts";
  import { confirm } from "../modal.ts";
  import type { Account, AccountProviderInfo, AccountUsage, AccountsConnectResponse, ConnectFlow, RoomSummary } from "../contract.gen.ts";
  import Icon from "./Icon.svelte";
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import AccountMeter from "./AccountMeter.svelte";
  import ItemList from "./settings/ItemList.svelte";
  import ListDetail from "./settings/ListDetail.svelte";
  import SearchBox from "./settings/SearchBox.svelte";
  import RoomsServed from "./settings/RoomsServed.svelte";
  // Settings > Accounts (spec 5.13): every model account by provider, a row of values with its usage at a glance, and
  // the open account's detail, the one place that explains its usage, rooms, sign-in and removal. Add an account opens
  // the providers in the detail's place, then the chosen provider's sign-in or key form there. accounts.list is whole,
  // so the list searches and filters here. The pane is admin only (routes.ts), as every account change is owner-class.
  let { bar, phone, signedout, selected, onselect, onclose }: {
    bar: Snippet<[string, number | undefined, Snippet | undefined]>; phone: boolean; signedout: () => void;
    selected: string | undefined; onselect: (id: string, open: boolean) => void; onclose: () => void;
  } = $props();
  let providers: AccountProviderInfo[] = $state.raw([]), accounts: Account[] | undefined = $state.raw(), rooms: RoomSummary[] = $state.raw([]), usage: AccountUsage[] = $state.raw([]);
  let failed = $state(false), busy = $state(false), loadedAt = $state(Date.now());
  // A failed change on the open account, in the page's words.
  let problem = $state("");
  let items = $derived(accounts && accountItems(accounts, providers, usage));
  let item = $derived(items?.find(each => each.account.id === selected));

  // Add: the provider picked (none for the providers, "new" for Add provider) and its form.
  let creating = $state(false), picked: string | undefined = $state(), query = $state(""), expanded = $state(false);
  let label = $state(""), secret = $state(""), useKey = $state(false), providerName = $state(""), providerAddress = $state(""), modelText = $state("");
  let provider = $derived(providers.find(each => each.id === picked));
  let flow: ConnectFlow | undefined = $derived(useKey ? "key" : provider?.connectFlow);
  let choices = $derived(providerChoices(providers, query, expanded));

  // A sign-in that waits, for Add or for one account's Sign in again (reconnect names it). generation drops the answers
  // of a sign-in that was left.
  let attempt: AccountsConnectResponse | undefined = $state(), reconnect: string | undefined = $state();
  let connectState: "idle" | "pending" | "failed" | "expired" = $state("idle");
  let error = $state(""), redirect = $state(""), copied = $state(false), opened = $state(false), now = $state(Date.now());
  let timer: ReturnType<typeof setTimeout> | undefined, generation = 0;

  // The open account's label field.
  let saved = $derived(item?.account.label);
  let labelDraft = $state(""), labelError = $state(""), labelSaving = $state(false);
  // Inside the detail, to hand its panel the focus when what had it is gone.
  let anchor: HTMLElement | undefined = $state();
  const gone = (reason: unknown) => { if (reason instanceof SignedOut) signedout(); return reason instanceof SignedOut; };
  const panel = async () => { await tick(); anchor?.closest<HTMLElement>("aside")?.focus(); };

  // Resolves once the list has loaded or failed; the usage and the rooms fill in when they come.
  function reload() {
    void call("accounts.quota", {}).then(result => { usage = result.accounts ?? []; loadedAt = Date.now(); }, () => {});
    void call("room.list", {}).then(result => { rooms = result.rooms ?? []; }, () => {});
    return call("accounts.list", {}).then(result => { providers = result.providers ?? []; accounts = result.accounts ?? []; failed = false; },
      (reason: unknown) => { if (!gone(reason) && !accounts) failed = true; });
  }
  function refresh() { void reload(); }
  function cancel() {
    generation++; clearTimeout(timer);
    attempt = undefined; reconnect = undefined; connectState = "idle"; error = ""; redirect = ""; copied = false; opened = false;
  }
  // Add is a step in history of its own, so Back leaves it as the back arrow does.
  function popped() { if (creating && !history.state?.creating) { creating = false; cancel(); } }
  onMount(() => {
    refresh();
    window.addEventListener("focus", refresh);
    window.addEventListener("popstate", popped);
    return () => { cancel(); window.removeEventListener("focus", refresh); window.removeEventListener("popstate", popped); };
  });
  // Another account, or none, starts without the last one's sign-in and problem.
  $effect.pre(() => { void selected; cancel(); problem = ""; });
  $effect.pre(() => { labelDraft = saved ?? ""; labelError = ""; });

  function start() {
    problem = "";
    // An open account stays in the path under the form, so Back returns to it.
    if (!creating) { cancel(); picked = undefined; query = ""; history.pushState({ creating: true, overList: selected === undefined }, "", location.href); }
    creating = true;
    void panel();
  }
  // On a phone the back arrow is the provider form's one way back, so it returns to the providers first.
  function close() {
    cancel();
    if (creating && phone && picked !== undefined) { choose(undefined); return; }
    if (!creating) { onclose(); return; }
    if (history.state?.creating) history.back();
    else creating = false;
  }
  // An account picked while Add is up takes Add's history step, so none is left behind that Back would land on.
  function select(id: string, open: boolean) {
    if (creating && history.state?.creating) history.replaceState({ fromList: !!history.state.overList }, "", location.href);
    creating = false;
    onselect(id, open);
  }
  function choose(id: string | undefined) {
    cancel(); picked = id; label = ""; secret = ""; useKey = false;
    void panel();
  }
  // A finished add opens the new account once the list holds it, in Add's history step, unless Add was left meanwhile.
  async function added(id: string | undefined) {
    cancel();
    await reload();
    if (!creating) return;
    creating = false;
    if (history.state?.creating) history.replaceState({ fromList: !!history.state.overList }, "", location.href);
    if (id && accounts?.some(each => each.id === id)) { onselect(id, false); void panel(); }
  }

  function watch(id: string, ticket: number) {
    timer = setTimeout(() => {
      now = Date.now();
      void call("accounts.connect_status", { attempt: id }).then(result => {
        if (ticket !== generation) return;
        if (result.status === "connected") { if (reconnect) { cancel(); refresh(); } else void added(result.accountId); }
        else if (result.status === "pending") watch(id, ticket);
        else { connectState = result.status; error = result.reason === "wrong_account" ? "That sign-in belongs to a different account." : result.reason === "adoption_failed" ? "Dyna cannot reconnect this sign-in. Remove it and add it again." : ""; }
      }, () => { if (ticket === generation) { connectState = "failed"; error = "Could not reach the service. Try again."; } });
    }, 2000);
  }
  function remaining(at: string) {
    const seconds = Math.max(0, Math.ceil((Date.parse(at) - now) / 1000));
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
  }
  // Add's sign-in or key, or an account's Sign in again. The sign-in page opens in a tab made in the click, as a browser
  // blocks one opened after the call returns.
  async function begin(again?: Account) {
    const chosen = again ? providers.find(each => each.id === again.provider) : provider;
    if (!chosen || busy) return;
    cancel();
    busy = true; const ticket = generation;
    if (!again) label = label.trim() || `${chosen.displayName} account`;
    if (!again && flow === "key") {
      const key = secret; secret = "";
      await call("accounts.set_key", { provider: chosen.id, label, key }).then(result => { if (ticket === generation) void added(result.accountId); },
        (reason: unknown) => { if (!gone(reason) && ticket === generation) error = "Could not save the key. Try again."; });
      busy = false;
      return;
    }
    const popup = window.open("", "_blank");
    if (popup) popup.opener = null;
    await (again ? call("accounts.reconnect", { accountId: again.id }) : call("accounts.connect", { provider: chosen.id, label })).then(result => {
      if (ticket !== generation) { popup?.close(); return; }
      attempt = result; reconnect = again?.id; connectState = "pending"; now = Date.now(); watch(result.attempt, ticket);
      // The button that started it is gone, so the panel keeps the focus, and Esc there cancels.
      void panel();
      if (popup) { popup.location.href = result.link; opened = true; }
    }, (reason: unknown) => {
      popup?.close();
      if (!gone(reason) && ticket === generation) { connectState = "failed"; error = reconnectUnavailable(reason) ? "Dyna cannot reconnect this sign-in. Remove it and add it again." : refused(reason) ? "The provider did not accept the sign-in." : "Could not reach the service. Try again."; }
    });
    busy = false;
  }
  async function finish() {
    if (!attempt || busy || !redirect.trim()) return;
    const ticket = generation, url = redirect; redirect = ""; busy = true;
    await call("accounts.connect_callback", { attempt: attempt.attempt, redirectUrl: url }).catch((reason: unknown) => { if (!gone(reason) && ticket === generation) error = "Could not finish sign-in. Try again."; });
    busy = false;
  }
  async function addProvider() {
    if (busy) return;
    busy = true; error = "";
    const displayName = providerName.trim();
    await call("accounts.set_key", { displayName, baseUrl: providerAddress.trim(), label: label.trim() || displayName, key: secret, models: parseModelIDs(modelText) }).then(result => {
      providerName = ""; providerAddress = ""; modelText = ""; secret = "";
      void added(result.accountId);
    }, (reason: unknown) => { if (!gone(reason)) error = addProviderError(reason); });
    busy = false;
  }

  // Enter and leaving the field both save, so the blur after an Enter finds nothing new. A refused label keeps the
  // owner's text in the field.
  async function saveLabel(account: Account) {
    const next = labelDraft.trim();
    if (labelSaving) return;
    if (next === account.label) { labelDraft = account.label; labelError = ""; return; }
    labelSaving = true; labelError = "";
    await call("accounts.rename", { accountId: account.id, label: next }).then(() => {
      accounts = accounts?.map(each => each.id === account.id ? { ...each, label: next } : each);
      refresh();
    }, (reason: unknown) => { if (!gone(reason) && selected === account.id) labelError = labelRefusal(reason); });
    labelSaving = false;
  }
  // Whether the engine took the rooms.
  async function setRooms(account: Account, ids: string[]) {
    busy = true; problem = "";
    const done = await call("accounts.set_limits", { accountId: account.id, rooms: ids }).then(async () => { await reload(); return true; },
      (reason: unknown) => { if (!gone(reason)) problem = "Room limits could not be saved. Try again."; return false; });
    busy = false;
    return done;
  }
  async function remove(each: AccountItem) {
    problem = "";
    const ok = await confirm({ title: `Remove ${each.name}?`, message: removalPrompt(each.provider?.connectMethod ?? "key"), action: "Remove", danger: true, failed: "The account was not removed. Try again.",
      run: () => call("accounts.remove", { accountId: each.account.id }).catch((reason: unknown) => { gone(reason); throw reason; }) });
    if (!ok) return;
    onclose();
    await reload();
  }
  // Esc puts back a changed label, then cancels a sign-in that waits, before it closes the detail.
  function escape(event: KeyboardEvent) {
    if (event.key !== "Escape" || event.isComposing || event.defaultPrevented) return;
    const field = (event.target as Element).closest("input.label");
    if (field && saved !== undefined && labelDraft !== saved) { event.preventDefault(); labelDraft = saved; labelError = ""; }
    else if (attempt) { event.preventDefault(); cancel(); }
  }
  const ways: Record<ConnectFlow, string> = { callback: "Sign in with your plan", device: "Sign in with a code", key: "API key" };
  const connected: Record<ConnectFlow, string> = { callback: "signed in with a plan", device: "signed in with a code", key: "API key" };
  const plural = (n: number, one: string, many: string) => `${n.toLocaleString("en")} ${n === 1 ? one : many}`;
  const initial = (name: string) => name.slice(0, 1).toUpperCase();
  const clock = (at: string) => new Date(at).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
</script>

{#snippet row(each: AccountItem, compact: boolean)}{@const pill = accountPill(each.account)}{@const hint = usageHint(each, loadedAt)}
  {@const pname = each.provider?.displayName ?? each.account.provider}<Avatar kind="room" name={pname} label={initial(pname)} size={24} />
  <span class="lines"><span class="title"><span class="name" title={each.name}>{each.name}</span>{#if pill}<StatusPill tone={pill.tone} label={pill.label} />{/if}</span>
    <span class="line">{accountLine(each)}</span></span>
  {#if hint}<span class="hint" class:compact title={hint.bars.map(bar => `${bar.label} ${bar.percent}%`).join(" · ")}>
    {#each hint.bars as bar (bar.label)}<span class="usebar" class:busy={bar.busy}><i style:width="{bar.percent}%"></i></span>{/each}<span class="peak">{hint.peak}%</span></span>{/if}
{/snippet}

{#snippet empty()}<button type="button" class="btn" onclick={start}>Add an account</button>{/snippet}

{#snippet list({ drill, select: pick }: { drill: boolean; select: (key: string, open: boolean) => void })}
  <ItemList {items} {failed} retry={() => { failed = false; refresh(); }} key={each => each.account.id} noun={["account", "accounts"]} label="Accounts"
    text={each => `${each.name} ${each.provider?.displayName ?? each.account.provider} ${(each.account.models ?? []).join(" ")}`}
    chips={accountChips} group={each => ({ id: each.account.provider, title: each.provider?.displayName ?? each.account.provider })}
    {row} {empty} selected={creating ? undefined : selected} {drill} onselect={pick} />
{/snippet}

<!-- A sign-in that waits: the code to enter, or the two steps of a plan sign-in. -->
{#snippet waiting(open: AccountsConnectResponse, name: string)}
  {#if open.flow === "device"}
    <p>Open the provider's page and enter this code.</p>
    <div class="code"><strong>{open.code}</strong><button class="btn ghost" type="button" onclick={() => { void navigator.clipboard.writeText(open.code ?? ""); copied = true; }}>{copied ? "Copied" : "Copy"}</button></div>
    <p><a href={open.link} target="_blank" rel="noopener noreferrer">Open sign-in ↗</a></p>
    <p class="small muted">Waiting for you to approve · Expires in {remaining(open.expiresAt)}</p>
    <div class="actions"><button type="button" class="btn" onclick={() => { cancel(); void panel(); }}>Cancel</button></div>
  {:else}
    <div class="step"><span class="badge-step" class:checked={opened} aria-hidden="true">{#if opened}<Icon name="check" size={14} />{:else}1{/if}</span><p>Open the {name} sign-in and approve Dyna.<br />
      <a href={open.link} target="_blank" rel="noopener noreferrer" onclick={() => opened = true}>{opened ? "Opened in your browser" : "Open sign-in"} ↗</a>
      <button class="btn ghost" type="button" onclick={() => { void navigator.clipboard.writeText(open.link); copied = true; }}>{copied ? "Copied" : "Copy link"}</button></p></div>
    <form class="form" onsubmit={event => { event.preventDefault(); void finish(); }}>
      <div class="step"><span class="badge-step" aria-hidden="true">2</span><label for="callback-address">Paste the address of the page you land on. That page may say it cannot be reached; that is expected.</label></div>
      <input id="callback-address" class="input" aria-label="Address of the page you land on" bind:value={redirect} placeholder="http://localhost/…" autocomplete="off" />
      <div class="actions"><button class="btn fill" type="submit" disabled={busy || !redirect.trim()}>Finish</button><button type="button" class="btn" onclick={() => { cancel(); void panel(); }}>Cancel</button></div>
    </form>
    <p class="small muted">{#if !phone}Enter finishes. Escape, or leaving this page, cancels.{" "}{/if}The address holds a one-time code. Dyna hands it to the sign-in service and never keeps it.{#if open.expiresAt}{" "}Expires in {remaining(open.expiresAt)}.{/if}</p>
  {/if}
{/snippet}

{#snippet failure()}
  {#if connectState === "expired"}<p class="problem" role="alert">The sign-in expired. Cancel and try again.</p>{/if}
  {#if error}<p class="problem" role="alert">{error}</p>{/if}
{/snippet}

{#snippet providerList()}
  <SearchBox bind:value={query} label="Search providers" placeholder={`Search ${plural(providers.length, "provider", "providers")}`} />
  <div class="provs">
    {#each choices.items as each (each.id + each.displayName)}{@const n = accounts?.filter(a => a.provider === each.id).length ?? 0}
      <button type="button" class="prov" onclick={() => choose(each.id)}><Avatar kind="room" name={each.displayName} label={initial(each.displayName)} size={24} /><span class="pname">{each.displayName}</span>{#if n}<span class="muted small">{n}</span>{/if}</button>{/each}
    {#if choices.more}<button type="button" class="btn ghost more" onclick={() => expanded = true}>Show {choices.more} more</button>{/if}
    {#if !choices.items.length}<p class="small muted">No provider matches “{query.trim()}”.</p>{/if}
    <button type="button" class="prov" onclick={() => choose("new")}><span class="initial" aria-hidden="true">+</span><span class="pname">Add provider</span><span class="muted small">OpenAI-compatible</span></button>
  </div>
  <div class="section"><strong class="head">Ways to connect</strong><div class="ways">
    <p><b>Sign in with your plan</b>You approve Dyna on the provider's page, then paste back the address it lands on. Dyna sees your plan's limits, not your chats.</p>
    <p><b>Sign in with a code</b>Dyna shows a short code. You enter it on the provider's page, and Dyna finishes by itself.</p>
    <p><b>API key</b>You paste a key once. It stays where Dyna runs, and Dyna never shows it again.</p></div></div>
{/snippet}

{#snippet addForm()}
  {#if !phone}<button type="button" class="btn ghost back" onclick={() => choose(undefined)}><Icon name="back" size={16} />All providers</button>{/if}
  {#if picked === "new"}
    <form class="form" onsubmit={event => { event.preventDefault(); void addProvider(); }}>
      <p class="small muted">Any OpenAI-compatible service. Dyna sends model requests to the address you enter.</p>
      <label class="field">Name<input class="input" bind:value={providerName} placeholder="Acme" autocomplete="off" disabled={busy} /></label>
      <label class="field">Address<input class="input mono" bind:value={providerAddress} placeholder="https://api.example.com/v1" autocomplete="off" inputmode="url" disabled={busy} /></label>
      <label class="field">Label<input class="input" bind:value={label} placeholder="Work, Personal, …" disabled={busy} /></label>
      <label class="field">API key<input class="input" type="password" bind:value={secret} autocomplete="off" disabled={busy} /></label>
      <label class="field">Models<textarea class="input" bind:value={modelText} rows="3" disabled={busy}></textarea><span class="help">Leave it empty to read them from the provider, or enter one model ID per line.</span></label>
      {@render failure()}
      <div class="actions"><button class="btn fill" type="submit" disabled={busy || !providerName.trim() || !providerAddress.trim() || !secret.trim()}>Add provider</button></div>
    </form>
  {:else if provider && flow}
    <form class="form" onsubmit={event => { event.preventDefault(); void begin(); }}>
      <p class="small muted">{ways[flow]}</p>
      <label class="field">Label<input class="input" bind:value={label} placeholder="Work, Personal, …" disabled={busy || !!attempt} /></label>
      {#if flow === "key"}<label class="field">API key<input class="input" type="password" bind:value={secret} autocomplete="off" disabled={busy} /></label>{/if}
      {#if !attempt}<div class="actions"><button class="btn fill" type="submit" disabled={busy || (flow === "key" && !secret.trim())}>{flow === "callback" ? `Sign in with ${provider.displayName}` : flow === "device" ? "Get a code" : "Save key"}</button>
        {#if provider.alsoKey}<button class="btn" type="button" disabled={busy} onclick={() => { useKey = !useKey; secret = ""; }}>{useKey ? "Sign in instead" : "Use an API key"}</button>{/if}</div>{/if}
    </form>
    {#if attempt}{@render waiting(attempt, provider.displayName)}{/if}
    {@render failure()}
  {:else}<p class="muted">That provider is not offered any more.</p>
  {/if}
{/snippet}

{#snippet account(each: AccountItem)}{@const a = each.account}{@const name = each.provider?.displayName ?? a.provider}{@const note = usageNote(each)}
  {@const windows = a.state === "sign_in_expired" ? [] : visibleWindows(each.usage?.windows ?? [])}{@const balances = a.state === "sign_in_expired" ? [] : each.usage?.balances ?? []}
  {@const others = items?.some(o => o.account.provider === a.provider && o.account.id !== a.id)}
  <p class="small muted">{name} · {connected[each.provider?.connectFlow ?? "key"]}</p>
  <label class="field">Label<input class="input label" aria-invalid={!!labelError} autocomplete="off" bind:value={labelDraft} readonly={labelSaving}
    onblur={() => void saveLabel(a)} onkeydown={event => { if (event.key === "Enter" && !event.isComposing) { event.preventDefault(); void saveLabel(a); } }} />
    {#if labelError}<span class="problem" role="alert">{labelError}</span>{/if}</label>
  <div class="section"><strong class="head">Usage{#if each.usage?.observedAt && windows.length}<span class="at">· at {clock(each.usage.observedAt)}</span>{/if}</strong>
    {#if note}<p class="small muted">{note}</p>{/if}
    {#if windows.length}<div class="meters">{#each windows as window (window.name)}<AccountMeter {window} now={loadedAt} />{/each}</div>{/if}
    {#if balances.length}<p class="small">Balance {balancesText(balances)}</p>{/if}</div>
  {#if a.models?.length}<div class="section"><strong class="head">Models</strong><p class="small models">{a.models.join(" · ")}</p></div>{/if}
  <RoomsServed label="Serves" {rooms} chosen={a.rooms ?? []} {busy} set={ids => setRooms(a, ids)} />
  {#if each.provider?.connectMethod === "oauth"}<div class="section"><strong class="head">Sign-in</strong>
    {#if attempt && reconnect === a.id}{@render waiting(attempt, name)}
    {:else}<div class="actions"><button type="button" class="btn" class:fill={a.state === "sign_in_expired"} disabled={busy} onclick={() => void begin(a)}>Sign in again</button></div>
      <p class="small muted">For a changed plan, or when Dyna asks.</p>{/if}
    {@render failure()}</div>{/if}
  {#if problem}<p class="problem" role="alert">{problem}</p>{/if}
  <div class="danger-zone"><span class="small muted">{others ? `Its sessions move to another ${name} account at their next model call.` : "You can connect it again later."}</span>
    <button type="button" class="btn danger" aria-haspopup="dialog" disabled={busy} onclick={() => void remove(each)}>Remove</button></div>
{/snippet}

{#snippet detail()}
  <div class="detail" bind:this={anchor}>
    {#if creating}{#if picked === undefined}{@render providerList()}{:else}{@render addForm()}{/if}
    {:else if item}{#key item.account.id}{@render account(item)}{/key}
    {:else}<p class="muted">That account is gone, or the link is wrong.</p>{/if}
  </div>
{/snippet}

{#snippet summary()}
  {#if items?.length}{@const expired = items.filter(each => each.account.state === "sign_in_expired").length}
    <p class="muted"><span class="fact">{plural(items.length, "account", "accounts")}</span> <span class="fact">· {plural(new Set(items.map(each => each.account.provider)).size, "provider", "providers")}</span>
      {#if expired}<span class="fact">· <StatusPill tone="error" label={`${expired} sign-in expired`} /></span>{/if}</p>{/if}
{/snippet}

{#snippet action()}<button type="button" class="btn add" onclick={start}>{#if phone}<Icon name="plus" size={16} />Add{:else}Add an account{/if}</button>{/snippet}
{#snippet header()}{@render bar("Accounts", accounts?.length, action)}{/snippet}

<!-- svelte-ignore a11y_no_static_element_interactions: Esc is caught on its way down, before ListDetail closes the detail with it. -->
<div class="keys" onkeydowncapture={escape}>
<ListDetail bar={header} label="Accounts" title={creating ? (picked === "new" ? "Add provider" : provider ? `Add a ${provider.displayName} account` : "Add an account") : item?.name ?? "Not found"}
  open={creating || (!!accounts && selected !== undefined)} onselect={select} onclose={close} {list} {detail} {summary} />
</div>

<style>
  .keys { display: contents; }
  .add { margin-left: auto; gap: 4px; }
  .initial { display: inline-grid; place-items: center; flex: none; width: 24px; height: 24px; border-radius: 6px; background: var(--selected); color: var(--muted); font-size: 12px; font-weight: 600; }
  /* A row's usage: a short bar per window and the highest percent, all values. */
  .hint { flex: none; display: flex; align-items: center; gap: 8px; }
  .usebar { display: block; width: 44px; height: 5px; border-radius: 3px; background: var(--track); overflow: hidden; }
  .compact .usebar { width: 26px; }
  .usebar i { display: block; height: 100%; border-radius: inherit; background: var(--muted); }
  .usebar.busy i { background: var(--attention-fill); }
  .peak { min-width: 34px; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; text-align: right; }
  .detail { display: grid; gap: 18px; min-width: 0; overflow-wrap: anywhere; }
  .head { font-size: 14px; }
  .at { margin-left: 6px; color: var(--muted); font-size: 12.5px; font-weight: 400; }
  .problem { margin: 0; color: var(--danger); font-size: 13px; font-weight: 500; }
  .mono { font-family: var(--mono); font-size: 13px; }
  .models { color: var(--text); }
  .form { display: grid; gap: 14px; min-width: 0; }
  .field { display: grid; gap: 6px; min-width: 0; font-size: 14px; font-weight: 600; }
  .help { color: var(--muted); font-size: 12.5px; font-weight: 400; line-height: 1.35; }
  .input { box-sizing: border-box; width: 100%; min-height: 34px; padding: 0 10px; border: 1px solid var(--firm); border-radius: 6px; background: var(--bg); color: var(--text); font: inherit; font-size: 14px; font-weight: 400; }
  textarea.input { padding: 8px 10px; resize: vertical; }
  .input:focus { outline: none; border-color: var(--focus); }
  .input[aria-invalid="true"] { border-color: var(--danger); }
  .meters { display: grid; gap: 14px; }
  .back { justify-self: start; gap: 6px; margin-left: -12px; }
  .provs { display: grid; gap: 2px; }
  .prov { display: flex; align-items: center; gap: 10px; min-height: 40px; padding: 0 8px; border: 0; border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 14px; text-align: left; cursor: pointer; }
  .prov:hover { background: var(--hover); }
  .prov:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .pname { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .more { justify-self: start; }
  .ways { display: grid; gap: 8px; }
  .ways p { display: grid; gap: 2px; padding: 10px 12px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); color: var(--muted); font-size: 12.5px; line-height: 1.4; }
  .ways b { color: var(--text); font-size: 13.5px; }
  .code { display: flex; align-items: center; gap: 12px; font-family: var(--mono); font-size: 20px; }
  .step { display: flex; align-items: flex-start; gap: 10px; font-size: 14px; line-height: 1.45; }
  .badge-step { display: inline-grid; place-items: center; flex: none; width: 22px; height: 22px; border-radius: 50%; background: var(--selected); color: var(--muted); font-size: 12px; }
  .badge-step.checked { box-sizing: border-box; border: 1px solid var(--line); background: none; }
  a { color: var(--focus); }
  @media (hover: none) { .input { min-height: 40px; } .prov { min-height: 44px; } }
</style>

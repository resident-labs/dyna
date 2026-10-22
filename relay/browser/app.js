const app = document.getElementById("app") || (() => { throw new Error("missing app element"); })();
const pageName = app.dataset.page || "/";

/**
 * @template {keyof HTMLElementTagNameMap} T
 * @param {T} tag
 * @param {Record<string, string | boolean | EventListener | undefined>} attrs
 * @param {...any} kids
 * @returns {HTMLElementTagNameMap[T]}
 */
function el(tag, attrs = {}, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "onclick") e.addEventListener("click", /** @type {EventListener} */ (v));
    else if (k === "class") e.className = String(v);
    else if (v !== undefined && v !== null && v !== false) e.setAttribute(k, v === true ? "" : String(v));
  }
  for (const k of kids.flat()) if (k !== null && k !== undefined && k !== false) e.append(k instanceof Node ? k : document.createTextNode(String(k)));
  return e;
}
/** @param {...(Node | Node[])} kids */
function show(...kids) { app.replaceChildren(...kids.flat()); }
/** @param {string | number | null} t */
function when(t) { return t ? new Date(t).toLocaleString() : "never"; }

class ApiError extends Error {
  /** @param {string} msg @param {string} code @param {number} status */
  constructor(msg, code, status) { super(msg); this.code = code; this.status = status; }
}

/** @param {string} path @param {object} body @returns {Promise<any>} */
async function api(path, body = {}) {
  const res = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json", "X-RL": "1" }, body: JSON.stringify(body), credentials: "same-origin" });
  const j = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(j.error || ("error " + res.status), j.code, res.status);
  return j;
}

// --- WebAuthn JSON helpers (no library) ---
/** @param {ArrayBuffer} b */
const b2u = (b) => btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
/** @param {string} s */
const u2b = (s) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4)), (c) => c.charCodeAt(0));

/** @param {Omit<PublicKeyCredentialCreationOptions, "challenge" | "user" | "excludeCredentials"> & {challenge: string, user: Omit<PublicKeyCredentialUserEntity, "id"> & {id: string}, excludeCredentials?: PublicKeyCredentialDescriptorJSON[]}} o @returns {PublicKeyCredentialCreationOptions} */
function creationOptions(o) {
  return { ...o, challenge: u2b(o.challenge), user: { ...o.user, id: u2b(o.user.id) },
    excludeCredentials: (o.excludeCredentials || []).map((c) => (/** @type {PublicKeyCredentialDescriptor} */ ({ ...c, id: u2b(c.id) }))) };
}
/** @param {Omit<PublicKeyCredentialRequestOptions, "challenge" | "allowCredentials"> & {challenge: string, allowCredentials?: PublicKeyCredentialDescriptorJSON[]}} o @returns {PublicKeyCredentialRequestOptions} */
function requestOptions(o) {
  return { ...o, challenge: u2b(o.challenge), allowCredentials: (o.allowCredentials || []).map((c) => (/** @type {PublicKeyCredentialDescriptor} */ ({ ...c, id: u2b(c.id) }))) };
}
/** @param {PublicKeyCredential} c */
function regJSON(c) {
  const response = /** @type {AuthenticatorAttestationResponse} */ (c.response);
  return { id: c.id, rawId: b2u(c.rawId), type: c.type, clientExtensionResults: c.getClientExtensionResults(),
    authenticatorAttachment: c.authenticatorAttachment || undefined,
    response: { clientDataJSON: b2u(response.clientDataJSON), attestationObject: b2u(response.attestationObject),
      transports: response.getTransports ? response.getTransports() : [] } };
}
/** @param {PublicKeyCredential} c */
function authJSON(c) {
  const response = /** @type {AuthenticatorAssertionResponse} */ (c.response);
  return { id: c.id, rawId: b2u(c.rawId), type: c.type, clientExtensionResults: c.getClientExtensionResults(),
    authenticatorAttachment: c.authenticatorAttachment || undefined,
    response: { clientDataJSON: b2u(response.clientDataJSON), authenticatorData: b2u(response.authenticatorData),
      signature: b2u(response.signature), userHandle: response.userHandle ? b2u(response.userHandle) : undefined } };
}

/** @param {Omit<PublicKeyCredentialCreationOptions, "challenge" | "user" | "excludeCredentials"> & {challenge: string, user: Omit<PublicKeyCredentialUserEntity, "id"> & {id: string}, excludeCredentials?: PublicKeyCredentialDescriptorJSON[]}} options */
async function createPasskey(options) {
  const c = await navigator.credentials.create({ publicKey: creationOptions(options) });
  if (!c) throw new Error("no passkey was made");
  return regJSON(/** @type {PublicKeyCredential} */ (c));
}

async function signIn(purpose = "login") {
  const o = await api("/v1/auth/options", { purpose });
  const c = await navigator.credentials.get({ publicKey: requestOptions(o.options) });
  if (!c) throw new Error("no passkey was chosen");
  return api("/v1/auth/verify", { id: o.id, purpose, response: authJSON(/** @type {PublicKeyCredential} */ (c)) });
}

// withStepUp runs fn, and when the relay asks for a fresh passkey, asks
// for it and runs fn again.
/** @template T @param {() => Promise<T>} fn @returns {Promise<T>} */
async function withStepUp(fn) {
  try { return await fn(); } catch (e) {
    if (!(e instanceof ApiError) || e.code !== "step-up") throw e;
    await signIn("stepup");
    return fn();
  }
}

/** @param {unknown} e */
function errorBox(e) { return el("p", { class: "error", role: "alert" }, e instanceof Error ? e.message : String(e)); }

/** @param {HTMLButtonElement} btn @param {() => Promise<unknown>} fn */
function busy(btn, fn) {
  return async () => {
    btn.disabled = true;
    const old = document.querySelector(".error"); if (old) old.remove();
    try { await fn(); } catch (e) { btn.after(errorBox(e)); } finally { btn.disabled = false; }
  };
}

// --- sign in ---

/** @param {() => unknown} after */
function signInView(after) {
  const b = el("button", { class: "primary", id: "signin" }, "Sign in with a passkey");
  b.addEventListener("click", busy(b, async () => { await signIn(); await after(); }));
  show(el("h1", {}, "Sign in"),
    el("section", {}, el("p", {}, "Use the passkey you made when you joined."), b,
      el("p", { class: "muted small" }, "Lost every passkey? ", el("a", { href: "/recover" }, "Use a recovery code"), ".")));
}

/** @param {() => unknown} after */
async function needSession(after) {
  try { return await api("/v1/me"); } catch (e) {
    if (e instanceof ApiError && e.code === "sign-in") { signInView(after); return null; }
    throw e;
  }
}

// --- dashboard ---

async function dashboard() {
  const me = await needSession(dashboard);
  if (!me) return;
  /** @type {Node[]} */
  const out = [el("h1", {}, "Hello, ", me.user.name)];

  const ms = el("section", {}, el("h2", {}, "Machines"));
  if (!me.machines.length) ms.append(el("p", { class: "muted" }, "No machines yet. On a computer, run ", el("span", { class: "mono" }, "dyna engine connect link"), "."));
  for (const m of me.machines) {
    const open = el("button", { class: "primary", onclick: () => { location.href = m.app + "/"; } }, "Open");
    const un = el("button", { class: "danger" }, "Unlink");
    un.addEventListener("click", busy(un, async () => {
      if (!confirm("Unlink " + m.name + "? Its tunnel and names are removed, and every Connect session on it ends.")) return;
      await withStepUp(() => api("/v1/machines/unlink", { id: m.id })); dashboard();
    }));
    ms.append(el("div", { class: "row machine", "data-id": m.id },
      el("div", {}, el("b", {}, m.name), " ", el("span", { class: "pill" }, m.status), el("div", { class: "muted small mono" }, m.app)),
      el("div", { class: "actions" }, open, un)));
  }
  out.push(ms);

  const ds = el("section", {}, el("h2", {}, "Signed-in devices"),
    el("p", { class: "muted small" }, "Revoking a device also ends its sessions on your machines within a minute."));
  for (const d of me.devices) {
    const r = el("button", { class: "danger" }, d.current ? "Sign out" : "Revoke");
    r.addEventListener("click", busy(r, async () => {
      if (d.current) { await api("/v1/auth/logout"); location.reload(); return; }
      await withStepUp(() => api("/v1/devices/revoke", { id: d.id })); dashboard();
    }));
    ds.append(el("div", { class: "row device", "data-id": d.id },
      el("div", {}, el("b", {}, d.name), d.current ? el("span", { class: "pill" }, "this device") : "",
        el("div", { class: "muted small" }, "signed in " + when(d.created) + " · seen " + when(d.lastSeen) + " · " + d.id)), r));
  }
  out.push(ds);

  const ps = el("section", {}, el("h2", {}, "Passkeys"));
  for (const p of me.passkeys) ps.append(el("div", { class: "row" }, el("div", {}, p.name, el("div", { class: "muted small" }, "added " + when(p.created) + " · used " + when(p.lastUsed)))));
  const add = el("button", {}, "Add a passkey");
  add.addEventListener("click", busy(add, async () => {
    const o = await withStepUp(() => api("/v1/passkeys/options"));
    await api("/v1/passkeys/verify", { id: o.id, response: await createPasskey(o.options), name: prompt("Name for this passkey", "second passkey") || "passkey" });
    dashboard();
  }));
  const rc = el("button", {}, "New recovery codes");
  rc.addEventListener("click", busy(rc, async () => {
    const r = await withStepUp(() => api("/v1/recovery-codes"));
    showCodes(r.recoveryCodes, dashboard);
  }));
  ps.append(el("p", { class: "muted small" }, me.recoveryCodesLeft + " recovery codes left."), el("div", { class: "actions" }, add, rc));
  out.push(ps);

  if (me.user.role === "admin") {
    const name = el("input", { placeholder: "Their name", id: "invite-name" });
    const role = el("select", { id: "invite-role" }, el("option", { value: "user" }, "user"), el("option", { value: "admin" }, "admin"));
    const make = el("button", { class: "primary", id: "invite-make" }, "Make invite link");
    const result = el("div", { id: "invite-result" });
    make.addEventListener("click", busy(make, async () => {
      const r = await withStepUp(() => api("/v1/invites", { name: name.value, role: role.value }));
      result.replaceChildren(el("p", {}, "Send this link to " + name.value + ". It works once, for 24 hours:"), el("p", { class: "mono small", id: "invite-url" }, r.url));
    }));
    const us = el("section", {}, el("h2", {}, "Accounts (admin)"),
      el("p", { class: "muted small" }, "A recovery invite lets that person add a passkey. It also gives whoever holds the link control of the account, so send it only to them."));
    for (const u of me.users) {
      const rb = el("button", {}, "Recovery invite");
      rb.addEventListener("click", busy(rb, async () => {
        const r = await withStepUp(() => api("/v1/invites", { kind: "recover", userId: u.id }));
        result.replaceChildren(el("p", {}, "Recovery link for " + u.name + " (24 hours, once):"), el("p", { class: "mono small" }, r.url));
      }));
      us.append(el("div", { class: "row" }, el("div", {}, u.name, " ", el("span", { class: "pill" }, u.role), el("div", { class: "muted small mono" }, u.id)), u.id === me.user.id ? "" : rb));
    }
    us.append(el("div", { class: "actions" }, name, role, make), result);
    out.push(us);
  }

  const as = el("section", {}, el("h2", {}, "Activity"));
  for (const a of me.audit) as.append(el("div", { class: "row small" }, el("span", {}, a.action, " ", el("span", { class: "muted" }, a.detail)), el("span", { class: "muted" }, when(a.at))));
  out.push(as);
  show(out);
}

/** @param {string[]} codes @param {() => unknown} next */
function showCodes(codes, next) {
  const b = el("button", { class: "primary" }, "I saved them");
  b.addEventListener("click", next);
  show(el("h1", {}, "Recovery codes"),
    el("section", {}, el("p", {}, "Each code works once and lets you add a new passkey if you lose all of yours. Using one signs out every device. Save them now; they are not shown again."),
      el("div", { class: "codes mono", id: "codes" }, codes.map((c) => el("div", {}, c))), el("div", { class: "actions" }, b)));
}

// --- enroll ---

async function enroll() {
  const token = location.hash.slice(1);
  if (!token) return show(el("h1", {}, "Join"), el("section", {}, el("p", {}, "Open the full invite link you got.")));
  const b = el("button", { class: "primary", id: "enroll" }, "Create a passkey");
  show(el("h1", {}, "Join Resident Labs Connect"),
    el("section", {}, el("p", {}, "This invite makes your account. Your passkey is the only way to sign in: no password, no email."), b));
  b.addEventListener("click", busy(b, async () => {
    const o = await api("/v1/enroll/options", { token });
    const r = await api("/v1/enroll/verify", { token, id: o.id, response: await createPasskey(o.options) });
    history.replaceState(null, "", "/enroll");
    if (r.recoveryCodes.length) {
      showCodes(r.recoveryCodes, () => {
        const add = el("button", { class: "primary" }, "Add a second passkey");
        const skip = el("button", { onclick: () => { location.href = "/"; } }, "Later");
        add.addEventListener("click", busy(add, async () => {
          const o2 = await withStepUp(() => api("/v1/passkeys/options"));
          await api("/v1/passkeys/verify", { id: o2.id, response: await createPasskey(o2.options), name: "second passkey" });
          location.href = "/";
        }));
        show(el("h1", {}, "One more passkey?"), el("section", {}, el("p", {}, "A second passkey, on another device or a security key, keeps you in if you lose this one."), el("div", { class: "actions" }, add, skip)));
      });
    } else location.href = "/";
  }));
}

// --- recover ---

function recover() {
  const code = el("input", { placeholder: "recovery code", class: "mono", id: "code" });
  const b = el("button", { class: "primary" }, "Make a new passkey");
  show(el("h1", {}, "Recover your account"),
    el("section", {}, el("p", {}, "A recovery code adds a new passkey and signs out every other device. Sessions on your machines end too."), code, el("div", { class: "actions" }, b)));
  b.addEventListener("click", busy(b, async () => {
    const o = await api("/v1/recover/options", { code: code.value });
    await api("/v1/recover/verify", { code: code.value, id: o.id, response: await createPasskey(o.options) });
    location.href = "/";
  }));
}

// --- link a machine ---

async function link() {
  const me = await needSession(link);
  if (!me) return;
  const code = new URLSearchParams(location.search).get("code") || "";
  let info;
  try { info = await api("/v1/link/info", { code }); } catch (e) { return show(el("h1", {}, "Link a machine"), el("section", {}, errorBox(e))); }
  const ok = el("button", { class: "primary", id: "approve" }, "Link to my account");
  const no = el("button", { class: "danger" }, "Deny");
  show(el("h1", {}, "Link a machine"),
    el("section", {},
      el("p", {}, "A computer named ", el("b", {}, info.name), " asks to join your account, ", me.user.name, "."),
      el("p", {}, "Check that the computer shows the same code and key:"),
      el("p", { class: "mono big", id: "link-code" }, info.code),
      el("p", { class: "mono", id: "link-fp" }, info.fingerprint),
      el("p", { class: "muted small" }, "Linking gives the computer a Cloudflare tunnel and two names on residentlabs.dev. Only your account can open it."),
      el("div", { class: "actions" }, ok, no)));
  ok.addEventListener("click", busy(ok, async () => {
    const r = await withStepUp(() => api("/v1/link/approve", { code }));
    show(el("h1", {}, "Linked"), el("section", {}, el("p", { class: "ok", id: "linked" }, r.machine.name + " is linked."),
      el("p", {}, "The computer finishes setup by itself. Then open it from your ", el("a", { href: "/" }, "machines list"), ".")));
  }));
  no.addEventListener("click", busy(no, async () => { await api("/v1/link/deny", { code }); show(el("h1", {}, "Denied"), el("section", {}, el("p", {}, "The request is closed."))); }));
}

// --- open a machine (the broker step) ---

async function openMachine() {
  const me = await needSession(openMachine);
  if (!me) return;
  const q = new URLSearchParams(location.search);
  const req = { machine: q.get("machine") || "", origin: q.get("origin") || "", jkt: q.get("jkt") || "", state: q.get("state") || "" };
  let info;
  try { info = await api("/v1/open/info", req); } catch (e) { return show(el("h1", {}, "Open a machine"), el("section", {}, errorBox(e))); }
  if (!info.allowed) return show(el("h1", {}, "Open a machine"), el("section", {}, el("p", { class: "error" }, info.origin + " is not an address that " + info.name + " reported.")));
  const b = el("button", { class: "primary", id: "open" }, "Open " + info.name);
  show(el("h1", {}, "Open ", info.name),
    el("section", {}, el("p", {}, "Sign this browser in to ", el("b", {}, info.name), " at ", el("span", { class: "mono" }, info.origin), " as ", me.user.name, "."),
      el("p", { class: "muted small" }, "The machine itself makes the sign-in code. It works once, for this browser only."), b));
  b.addEventListener("click", busy(b, async () => { const r = await api("/v1/open", req); location.replace(r.redirect); }));
}

/** @type {Record<string, () => unknown>} */
const routes = { "/": dashboard, "/signin": () => needSession(() => { location.href = "/"; }).then((m) => { if (m) location.href = "/"; }),
  "/enroll": enroll, "/recover": recover, "/link": link, "/open": openMachine };
Promise.resolve().then(routes[pageName] || dashboard).catch((e) => show(errorBox(e)));

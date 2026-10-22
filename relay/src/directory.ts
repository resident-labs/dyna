// Directory is the relay's one Durable Object, with SQLite storage. It holds
// accounts, passkeys, invites, relay devices (browser sessions on the
// relay), link requests, machines, replay nonces, rate counters and the
// audit log. One object keeps every check and claim in one transaction.
// That is right for a few hundred accounts; past that, split by user ID.
//
// It never holds room content, push subscriptions or tunnel tokens. It
// stores hashes of every bearer secret (sessions, invites, recovery codes,
// poll secrets), never the secret.

import { DurableObject } from "cloudflare:workers";

export const SESSION_LIFE = 30 * 24 * 3600_000;
export const STEP_UP_LIFE = 5 * 60_000;
export const INVITE_LIFE = 24 * 3600_000;
export const LINK_LIFE = 10 * 60_000;
export const CHALLENGE_LIFE = 5 * 60_000;

export type Role = "user" | "admin";

export interface User {
  id: string;
  name: string;
  role: Role;
  created: number;
}

export interface Session {
  id: string;
  userId: string;
  name: string;
  created: number;
  lastSeen: number;
  expires: number;
  steppedUpAt: number;
  revokedAt: number | null;
}

export interface Machine {
  id: string;
  userId: string;
  name: string;
  pubkey: string; // base64url raw Ed25519
  epoch: number;
  status: "provisioning" | "active" | "unlinked";
  created: number;
  lastSeen: number;
  tunnelId: string | null;
  generation: number;
  appPort: number;
  cardPort: number;
  routes: Route[];
  teardown: string | null; // what is left to remove after unlink
}

export interface Route {
  kind: string;
  app: string;
  cards: string;
}

export interface LinkRequest {
  id: string;
  userCode: string;
  pubkey: string;
  pollHash: string;
  name: string;
  appPort: number;
  cardPort: number;
  created: number;
  expires: number;
  approvedBy: string | null;
  machineId: string | null;
  status: "pending" | "approved" | "denied" | "done";
}

export interface Invite {
  name: string;
  role: Role;
  kind: "enroll" | "recover";
  userId: string | null;
  createdBy: string;
  expires: number;
}

export interface Credential {
  id: string;
  userId: string;
  publicKey: string; // base64url COSE
  counter: number;
  transports: string;
  name: string;
  created: number;
  lastUsed: number;
}

export interface AuditEntry {
  at: number;
  actor: string;
  action: string;
  detail: string;
}

type Row = Record<string, SqlStorageValue>;

export class Directory extends DurableObject {
  sql: SqlStorage;

  constructor(ctx: DurableObjectState, env: unknown) {
    super(ctx, env as never);
    this.sql = ctx.storage.sql;
    this.migrate();
  }

  private migrate() {
    this.sql.exec(`
      create table if not exists users (id text primary key, name text not null, role text not null, created integer not null);
      create table if not exists credentials (id text primary key, user_id text not null, public_key text not null, counter integer not null,
        transports text not null, name text not null, created integer not null, last_used integer not null);
      create table if not exists invites (hash text primary key, name text not null, role text not null, kind text not null, user_id text,
        created_by text not null, created integer not null, expires integer not null, used_at integer);
      create table if not exists recovery_codes (hash text primary key, user_id text not null, used_at integer);
      create table if not exists sessions (hash text primary key, id text unique not null, user_id text not null, name text not null,
        created integer not null, last_seen integer not null, expires integer not null, stepped_up_at integer not null, revoked_at integer);
      create table if not exists challenges (id text primary key, kind text not null, challenge text not null, user_id text, data text,
        expires integer not null);
      create table if not exists link_requests (id text primary key, user_code text unique not null, pubkey text not null, poll_hash text not null,
        name text not null, app_port integer not null, card_port integer not null, created integer not null, expires integer not null,
        approved_by text, machine_id text, status text not null);
      create table if not exists machines (id text primary key, user_id text not null, name text not null, pubkey text not null,
        epoch integer not null, status text not null, created integer not null, last_seen integer not null, tunnel_id text,
        generation integer not null, app_port integer not null, card_port integer not null, routes text not null, lan_ip text, teardown text);
      create table if not exists nonces (key text primary key, expires integer not null);
      create table if not exists rate (key text primary key, window integer not null, count integer not null);
      create table if not exists audit (seq integer primary key autoincrement, at integer not null, user_id text, actor text not null,
        action text not null, detail text not null);
    `);
  }

  private one(q: string, ...args: SqlStorageValue[]): Row | null {
    const rows = this.sql.exec(q, ...args).toArray();
    return rows.length ? (rows[0] as Row) : null;
  }

  private all(q: string, ...args: SqlStorageValue[]): Row[] {
    return this.sql.exec(q, ...args).toArray() as Row[];
  }

  private sweep(now: number) {
    this.sql.exec("delete from challenges where expires < ?", now);
    this.sql.exec("delete from nonces where expires < ?", now);
    this.sql.exec("delete from link_requests where expires < ? and status != 'approved'", now - 24 * 3600_000);
    this.sql.exec("delete from rate where window < ?", now - 3600_000);
  }

  // --- rate limits and replay ---

  // hit counts one event in a fixed window. It returns false once the
  // window holds more than limit events.
  hit(key: string, limit: number, windowMs: number, now = Date.now()): boolean {
    const w = Math.floor(now / windowMs) * windowMs;
    const r = this.one("select window, count from rate where key = ?", key);
    if (!r || (r.window as number) !== w) {
      this.sql.exec("insert or replace into rate (key, window, count) values (?, ?, 1)", key, w);
      return true;
    }
    const n = (r.count as number) + 1;
    this.sql.exec("update rate set count = ? where key = ?", n, key);
    return n <= limit;
  }

  // useNonce records a one-time value. It returns false if it was seen.
  useNonce(key: string, expires: number): boolean {
    this.sweep(Date.now());
    if (this.one("select 1 from nonces where key = ?", key)) return false;
    this.sql.exec("insert into nonces (key, expires) values (?, ?)", key, expires);
    return true;
  }

  // --- audit ---

  audit(userId: string | null, actor: string, action: string, detail: string) {
    this.sql.exec("insert into audit (at, user_id, actor, action, detail) values (?, ?, ?, ?, ?)", Date.now(), userId, actor, action, detail);
  }

  auditFor(userId: string, limit = 50): AuditEntry[] {
    return this.all("select at, actor, action, detail from audit where user_id = ? order by seq desc limit ?", userId, limit).map((r) => ({
      at: r.at as number,
      actor: r.actor as string,
      action: r.action as string,
      detail: r.detail as string,
    }));
  }

  // --- challenges (WebAuthn) ---

  putChallenge(id: string, kind: string, challenge: string, userId: string | null, data: string) {
    this.sweep(Date.now());
    this.sql.exec(
      "insert into challenges (id, kind, challenge, user_id, data, expires) values (?, ?, ?, ?, ?, ?)",
      id,
      kind,
      challenge,
      userId,
      data,
      Date.now() + CHALLENGE_LIFE,
    );
  }

  // takeChallenge spends a challenge: it works once.
  takeChallenge(id: string, kind: string): { challenge: string; userId: string | null; data: string } | null {
    const r = this.one("select challenge, user_id, data, expires from challenges where id = ? and kind = ?", id, kind);
    this.sql.exec("delete from challenges where id = ?", id);
    if (!r || (r.expires as number) < Date.now()) return null;
    return { challenge: r.challenge as string, userId: (r.user_id as string) ?? null, data: r.data as string };
  }

  // --- users and passkeys ---

  userCount(): number {
    return this.one("select count(*) as n from users")!.n as number;
  }

  user(id: string): User | null {
    const r = this.one("select * from users where id = ?", id);
    return r ? { id: r.id as string, name: r.name as string, role: r.role as Role, created: r.created as number } : null;
  }

  users(): User[] {
    return this.all("select * from users order by created").map((r) => ({
      id: r.id as string,
      name: r.name as string,
      role: r.role as Role,
      created: r.created as number,
    }));
  }

  credentials(userId: string): Credential[] {
    return this.all("select * from credentials where user_id = ? order by created", userId).map(credRow);
  }

  credential(id: string): Credential | null {
    const r = this.one("select * from credentials where id = ?", id);
    return r ? credRow(r) : null;
  }

  touchCredential(id: string, counter: number) {
    this.sql.exec("update credentials set counter = ?, last_used = ? where id = ?", counter, Date.now(), id);
  }

  addCredential(c: Credential) {
    this.sql.exec(
      "insert into credentials (id, user_id, public_key, counter, transports, name, created, last_used) values (?, ?, ?, ?, ?, ?, ?, ?)",
      c.id,
      c.userId,
      c.publicKey,
      c.counter,
      c.transports,
      c.name,
      c.created,
      c.lastUsed,
    );
  }

  // --- invites ---

  createInvite(hash: string, inv: Invite) {
    const now = Date.now();
    this.sql.exec(
      "insert into invites (hash, name, role, kind, user_id, created_by, created, expires) values (?, ?, ?, ?, ?, ?, ?, ?)",
      hash,
      inv.name,
      inv.role,
      inv.kind,
      inv.userId,
      inv.createdBy,
      now,
      inv.expires,
    );
    this.audit(inv.userId ?? inv.createdBy, inv.createdBy, inv.kind === "enroll" ? "invite.create" : "recovery-invite.create", inv.name);
    if (inv.kind === "recover" && inv.userId && inv.userId !== inv.createdBy) this.audit(inv.createdBy, inv.createdBy, "recovery-invite.create", inv.name);
  }

  peekInvite(hash: string): Invite | null {
    const r = this.one("select * from invites where hash = ? and used_at is null and expires > ?", hash, Date.now());
    if (!r) return null;
    return {
      name: r.name as string,
      role: r.role as Role,
      kind: r.kind as Invite["kind"],
      userId: (r.user_id as string) ?? null,
      createdBy: r.created_by as string,
      expires: r.expires as number,
    };
  }

  // redeemInvite spends an invite and adds the passkey in one step. An
  // enroll invite makes a new user; a recover invite adds a passkey to its
  // user and revokes every relay device of that user and bumps the epoch
  // of each of the user's machines, so Connect sessions must sign in again.
  redeemInvite(hash: string, cred: Omit<Credential, "userId">, newUserId: string): User | null {
    const inv = this.peekInvite(hash);
    if (!inv) return null;
    this.sql.exec("update invites set used_at = ? where hash = ?", Date.now(), hash);
    let userId: string;
    if (inv.kind === "enroll") {
      userId = newUserId;
      this.sql.exec("insert into users (id, name, role, created) values (?, ?, ?, ?)", userId, inv.name, inv.role, Date.now());
      this.audit(userId, userId, "account.create", `${inv.name} (${inv.role}), invited by ${inv.createdBy}`);
    } else {
      userId = inv.userId!;
      this.resetAccess(userId, "recovery invite");
    }
    this.addCredential({ ...cred, userId });
    this.audit(userId, userId, "passkey.add", cred.name);
    return this.user(userId);
  }

  // redeemBootstrap makes the first admin. It works only while no account
  // exists, checked here inside the one transaction, so the bootstrap
  // secret dies with the first account even if nobody deletes it.
  redeemBootstrap(cred: Omit<Credential, "userId">, userId: string, name: string): User | null {
    if (this.userCount() !== 0) return null;
    this.sql.exec("insert into users (id, name, role, created) values (?, ?, 'admin', ?)", userId, name, Date.now());
    this.audit(userId, userId, "account.create", `${name} (admin), bootstrap invite`);
    this.addCredential({ ...cred, userId });
    this.audit(userId, userId, "passkey.add", cred.name);
    return this.user(userId);
  }

  private resetAccess(userId: string, why: string) {
    const now = Date.now();
    this.sql.exec("update sessions set revoked_at = ? where user_id = ? and revoked_at is null", now, userId);
    this.sql.exec("update machines set epoch = epoch + 1 where user_id = ?", userId);
    this.audit(userId, userId, "account.reset", why + ": every relay device revoked, every machine epoch bumped");
  }

  // --- recovery codes ---

  setRecoveryCodes(userId: string, hashes: string[]) {
    this.sql.exec("delete from recovery_codes where user_id = ?", userId);
    for (const h of hashes) this.sql.exec("insert into recovery_codes (hash, user_id) values (?, ?)", h, userId);
    this.audit(userId, userId, "recovery-codes.set", `${hashes.length} codes`);
  }

  recoveryCodesLeft(userId: string): number {
    return this.one("select count(*) as n from recovery_codes where user_id = ? and used_at is null", userId)!.n as number;
  }

  recoveryCodeUser(hash: string): string | null {
    const r = this.one("select user_id from recovery_codes where hash = ? and used_at is null", hash);
    return r ? (r.user_id as string) : null;
  }

  // useRecoveryCode spends a code and adds a passkey, then resets access.
  useRecoveryCode(hash: string, cred: Omit<Credential, "userId">): User | null {
    const r = this.one("select user_id from recovery_codes where hash = ? and used_at is null", hash);
    if (!r) return null;
    const userId = r.user_id as string;
    this.sql.exec("update recovery_codes set used_at = ? where hash = ?", Date.now(), hash);
    this.resetAccess(userId, "recovery code");
    this.addCredential({ ...cred, userId });
    this.audit(userId, userId, "passkey.add", cred.name + " (recovery code)");
    return this.user(userId);
  }

  // --- relay devices (sessions) ---

  createSession(hash: string, id: string, userId: string, name: string): Session {
    const now = Date.now();
    this.sql.exec(
      "insert into sessions (hash, id, user_id, name, created, last_seen, expires, stepped_up_at) values (?, ?, ?, ?, ?, ?, ?, ?)",
      hash,
      id,
      userId,
      name,
      now,
      now,
      now + SESSION_LIFE,
      now,
    );
    this.audit(userId, id, "device.sign-in", name);
    return this.sessionById(id)!;
  }

  // session finds a live session by token hash and marks it seen.
  session(hash: string): Session | null {
    const r = this.one("select * from sessions where hash = ?", hash);
    if (!r) return null;
    const s = sessionRow(r);
    const now = Date.now();
    if (s.revokedAt || s.expires < now) return null;
    if (now - s.lastSeen > 60_000) this.sql.exec("update sessions set last_seen = ? where hash = ?", now, hash);
    return s;
  }

  sessionById(id: string): Session | null {
    const r = this.one("select * from sessions where id = ?", id);
    return r ? sessionRow(r) : null;
  }

  sessions(userId: string): Session[] {
    return this.all("select * from sessions where user_id = ? and revoked_at is null and expires > ? order by created", userId, Date.now()).map(
      sessionRow,
    );
  }

  stepUp(hash: string) {
    this.sql.exec("update sessions set stepped_up_at = ? where hash = ?", Date.now(), hash);
  }

  endSession(hash: string) {
    const r = this.one("select id, user_id from sessions where hash = ?", hash);
    this.sql.exec("update sessions set revoked_at = ? where hash = ? and revoked_at is null", Date.now(), hash);
    if (r) this.audit(r.user_id as string, r.id as string, "device.sign-out", r.id as string);
  }

  // revokeSession revokes one of the user's own relay devices.
  revokeSession(userId: string, id: string, actor: string): boolean {
    const r = this.one("select 1 from sessions where id = ? and user_id = ? and revoked_at is null", id, userId);
    if (!r) return false;
    this.sql.exec("update sessions set revoked_at = ? where id = ?", Date.now(), id);
    this.audit(userId, actor, "device.revoke", id);
    return true;
  }

  // --- link requests ---

  startLink(req: Omit<LinkRequest, "approvedBy" | "machineId" | "status">) {
    this.sweep(Date.now());
    this.sql.exec(
      "insert into link_requests (id, user_code, pubkey, poll_hash, name, app_port, card_port, created, expires, status) values (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')",
      req.id,
      req.userCode,
      req.pubkey,
      req.pollHash,
      req.name,
      req.appPort,
      req.cardPort,
      req.created,
      req.expires,
    );
  }

  linkByCode(code: string): LinkRequest | null {
    const r = this.one("select * from link_requests where user_code = ?", code);
    return r ? linkRow(r) : null;
  }

  linkById(id: string): LinkRequest | null {
    const r = this.one("select * from link_requests where id = ?", id);
    return r ? linkRow(r) : null;
  }

  // approveLink claims a pending request for a user and makes the machine
  // row. The machine ID comes from the relay, so nobody picks a host name.
  approveLink(code: string, userId: string, machineId: string): { link: LinkRequest; machine: Machine } | { error: string } {
    const l = this.linkByCode(code);
    const now = Date.now();
    if (!l || l.status !== "pending" || l.expires < now) return { error: "that code is not valid or has expired; run the link command again" };
    if (this.one("select 1 from machines where pubkey = ? and status != 'unlinked'", l.pubkey)) return { error: "this machine key is already linked" };
    this.sql.exec(
      "insert into machines (id, user_id, name, pubkey, epoch, status, created, last_seen, generation, app_port, card_port, routes) values (?, ?, ?, ?, 1, 'provisioning', ?, ?, 1, ?, ?, '[]')",
      machineId,
      userId,
      l.name,
      l.pubkey,
      now,
      now,
      l.appPort,
      l.cardPort,
    );
    this.sql.exec("update link_requests set status = 'approved', approved_by = ?, machine_id = ? where id = ?", userId, machineId, l.id);
    this.audit(userId, userId, "machine.link", `${l.name} (${machineId})`);
    return { link: this.linkById(l.id)!, machine: this.machine(machineId)! };
  }

  denyLink(code: string, userId: string): boolean {
    const l = this.linkByCode(code);
    if (!l || l.status !== "pending") return false;
    this.sql.exec("update link_requests set status = 'denied', approved_by = ? where id = ?", userId, l.id);
    this.audit(userId, userId, "machine.link-denied", l.name);
    return true;
  }

  finishLink(id: string) {
    this.sql.exec("update link_requests set status = 'done' where id = ?", id);
  }

  // --- machines ---

  machine(id: string): Machine | null {
    const r = this.one("select * from machines where id = ?", id);
    return r ? machineRow(r) : null;
  }

  machines(userId: string): Machine[] {
    return this.all("select * from machines where user_id = ? and status != 'unlinked' order by created", userId).map(machineRow);
  }

  touchMachine(id: string) {
    this.sql.exec("update machines set last_seen = ? where id = ?", Date.now(), id);
  }

  // recordTunnel is a provisioning checkpoint. It only writes when the
  // generation still matches, so a late retry cannot clobber newer work.
  recordTunnel(id: string, generation: number, tunnelId: string): boolean {
    const c = this.sql.exec("update machines set tunnel_id = ? where id = ? and generation = ? and status != 'unlinked'", tunnelId, id, generation);
    return c.rowsWritten > 0;
  }

  markActive(id: string, generation: number): boolean {
    const c = this.sql.exec("update machines set status = 'active' where id = ? and generation = ? and status = 'provisioning'", id, generation);
    return c.rowsWritten > 0;
  }

  setOrigin(id: string, appPort: number, cardPort: number): number {
    this.sql.exec("update machines set app_port = ?, card_port = ?, generation = generation + 1 where id = ?", appPort, cardPort, id);
    return this.machine(id)!.generation;
  }

  setRoutes(id: string, routes: Route[]) {
    this.sql.exec("update machines set routes = ? where id = ?", JSON.stringify(routes), id);
  }

  // unlink revokes first (status and epoch), then records what is left
  // to tear down, so a failed teardown never leaves a usable link.
  unlink(id: string, actor: string): Machine | null {
    const m = this.machine(id);
    if (!m || m.status === "unlinked") return null;
    this.sql.exec(
      "update machines set status = 'unlinked', epoch = epoch + 1, generation = generation + 1, teardown = 'pending' where id = ?",
      id,
    );
    this.audit(m.userId, actor, "machine.unlink", `${m.name} (${id})`);
    return this.machine(id);
  }

  setTeardown(id: string, generation: number, state: string | null) {
    this.sql.exec("update machines set teardown = ? where id = ? and generation = ?", state, id, generation);
  }

  // grant answers a machine's question: may this relay device still use
  // me? Only when the device is live and belongs to my owner.
  grant(machineId: string, deviceId: string): { active: boolean; epoch: number } {
    const m = this.machine(machineId);
    if (!m || m.status === "unlinked") return { active: false, epoch: m?.epoch ?? 0 };
    const s = this.sessionById(deviceId);
    const active = !!s && !s.revokedAt && s.expires > Date.now() && s.userId === m.userId;
    return { active, epoch: m.epoch };
  }
}

function credRow(r: Row): Credential {
  return {
    id: r.id as string,
    userId: r.user_id as string,
    publicKey: r.public_key as string,
    counter: r.counter as number,
    transports: r.transports as string,
    name: r.name as string,
    created: r.created as number,
    lastUsed: r.last_used as number,
  };
}

function sessionRow(r: Row): Session {
  return {
    id: r.id as string,
    userId: r.user_id as string,
    name: r.name as string,
    created: r.created as number,
    lastSeen: r.last_seen as number,
    expires: r.expires as number,
    steppedUpAt: r.stepped_up_at as number,
    revokedAt: (r.revoked_at as number) ?? null,
  };
}

function linkRow(r: Row): LinkRequest {
  return {
    id: r.id as string,
    userCode: r.user_code as string,
    pubkey: r.pubkey as string,
    pollHash: r.poll_hash as string,
    name: r.name as string,
    appPort: r.app_port as number,
    cardPort: r.card_port as number,
    created: r.created as number,
    expires: r.expires as number,
    approvedBy: (r.approved_by as string) ?? null,
    machineId: (r.machine_id as string) ?? null,
    status: r.status as LinkRequest["status"],
  };
}

function machineRow(r: Row): Machine {
  return {
    id: r.id as string,
    userId: r.user_id as string,
    name: r.name as string,
    pubkey: r.pubkey as string,
    epoch: r.epoch as number,
    status: r.status as Machine["status"],
    created: r.created as number,
    lastSeen: r.last_seen as number,
    tunnelId: (r.tunnel_id as string) ?? null,
    generation: r.generation as number,
    appPort: r.app_port as number,
    cardPort: r.card_port as number,
    routes: JSON.parse(r.routes as string),
    teardown: (r.teardown as string) ?? null,
  };
}

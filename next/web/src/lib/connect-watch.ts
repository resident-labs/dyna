import { allowedOrigin, deviceKey, getPin, hello, proof, verifyHello, type Pin } from "./connect.ts";

interface Routes { machine: string; current: string; routes: string[]; expires: string }
function isRoutes(value: unknown): value is Routes {
  if (typeof value !== "object" || value === null) return false;
  return "machine" in value && typeof value.machine === "string" && "current" in value && typeof value.current === "string" &&
    "expires" in value && typeof value.expires === "string" && "routes" in value && Array.isArray(value.routes) &&
    value.routes.every((route: unknown) => typeof route === "string" && allowedOrigin(route));
}
async function post(path: string, body: object): Promise<Response> {
  return fetch(path, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Dyna": "1" }, body: JSON.stringify(body) });
}

// The private routes response proves the server has authenticated this Connect session.
export async function watchConnect(): Promise<{ stream(up: boolean): void; stop(): void } | undefined> {
  const response = await post("/api/connect/routes", {}).catch(() => undefined);
  if (!response?.ok) return;
  const data: unknown = await response.json();
  if (!isRoutes(data) || data.current !== location.origin) return;
  const saved = await getPin(data.machine);
  if (!saved) return;
  const pin: Pin = saved;
  let stopped = false, moving = false, renewTimer: ReturnType<typeof setTimeout>, lostTimer: ReturnType<typeof setTimeout> | undefined;
  let expires = Date.parse(data.expires);
  async function renew() {
    if (stopped) return;
    const response = await post("/api/connect/renew", { proof: await proof(await deviceKey(), "/api/connect/renew") }).catch(() => undefined);
    if (response?.status === 401) { location.reload(); return; }
    if (response?.ok) {
      const value: unknown = await response.json();
      if (typeof value === "object" && value !== null && "expires" in value && typeof value.expires === "string") expires = Date.parse(value.expires);
    }
    arm(response?.ok ? Math.max(expires - Date.now() - 120_000, 30_000) : 30_000);
  }
  function arm(delay: number) { clearTimeout(renewTimer); if (!stopped) renewTimer = setTimeout(() => void renew(), delay); }
  async function trusted(origin: string, pin: Pin): Promise<boolean> {
    try {
      const greeting = await hello(origin);
      return !!greeting && greeting.machine === pin.machine && greeting.relay === pin.relay && greeting.key === pin.key && await verifyHello(greeting, pin.key);
    } catch { return false; }
  }
  async function find(routes: string[]) {
    const results = await Promise.all(routes.map(route => trusted(route, pin)));
    return routes.find((_, index) => results[index]);
  }
  function label(target: string): string {
    const host = new URL(target).hostname;
    if (host === "localhost" || host.endsWith(".localhost") || host === "127.0.0.1" || host === "[::1]") return "this computer";
    if (host.endsWith(".ts.net")) return "Tailscale";
    return "another HTTPS route";
  }
  async function move() {
    const here = data.routes.indexOf(data.current);
    if (here <= 0 || moving || stopped) return;
    const target = await find(data.routes.slice(0, here));
    if (!target || stopped || moving) return;
    moving = true;
    const path = "/api/connect/handoff";
    const result = await post(path, { target, proof: await proof(await deviceKey(), path) }).catch(() => undefined);
    if (!result?.ok) { moving = false; return; }
    const body: unknown = await result.json();
    if (typeof body !== "object" || body === null || !("code" in body) || typeof body.code !== "string") { moving = false; return; }
    const fragment = new URLSearchParams({ handoff: body.code, mid: pin.machine, mk: pin.key, relay: pin.relay, route: label(target), return: location.pathname + location.search });
    location.assign(target + "/connect#" + fragment);
  }
  async function fallback() {
    const target = await find(data.routes.filter(route => route !== data.current));
    if (target && !stopped) location.assign(target + location.pathname + location.search + "#" + new URLSearchParams({ route: label(target), lost: "1" }));
  }
  arm(Math.max(expires - Date.now() - 120_000, 1000));
  void move();
  return {
    stream(up) {
      if (up) { clearTimeout(lostTimer); lostTimer = undefined; }
      else if (!lostTimer) lostTimer = setTimeout(() => void fallback(), 20_000);
    },
    stop() { stopped = true; clearTimeout(renewTimer); clearTimeout(lostTimer); },
  };
}

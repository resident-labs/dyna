// Resident Labs Connect relay: accounts, machine links, managed tunnels,
// and the broker step for short-lived machine sessions. It is never in the
// traffic path between a browser and a machine. See docs/desktop/CONNECT.md.

import * as auth from "./auth";
import * as machines from "./machines";
import { errorResponse, HttpError, withSecurity, type Env } from "./http";
import { page, script, style } from "./ui";

export { Directory } from "./directory";

type Handler = (req: Request, env: Env, ctx: ExecutionContext) => Promise<Response>;

const api: Record<string, Handler> = {
  "/v1/enroll/options": auth.enrollOptions,
  "/v1/enroll/verify": auth.enrollVerify,
  "/v1/recover/options": auth.recoverOptions,
  "/v1/recover/verify": auth.recoverVerify,
  "/v1/auth/options": auth.authOptions,
  "/v1/auth/verify": auth.authVerify,
  "/v1/auth/logout": auth.logout,
  "/v1/passkeys/options": auth.passkeyOptions,
  "/v1/passkeys/verify": auth.passkeyVerify,
  "/v1/me": auth.me,
  "/v1/devices/revoke": auth.revokeDevice,
  "/v1/recovery-codes": auth.newRecoveryCodes,
  "/v1/invites": auth.createInvite,
  "/v1/link/info": machines.linkInfo,
  "/v1/link/approve": machines.linkApprove,
  "/v1/link/deny": machines.linkDeny,
  "/v1/machines/unlink": machines.unlinkByUser,
  "/v1/open/info": machines.openInfo,
  "/v1/open": machines.open,
  // Machine API: signed by the machine key, not by a cookie.
  "/v1/link/start": machines.linkStart,
  "/v1/link/poll": machines.linkPoll,
  "/v1/machine/status": machines.machineStatus,
  "/v1/machine/tunnel-token": machines.machineTunnelToken,
  "/v1/machine/grant": machines.machineGrant,
  "/v1/machine/routes": machines.machineRoutes,
  "/v1/machine/origin": machines.machineOrigin,
  "/v1/machine/unlink": machines.machineUnlink,
};

const pages = new Set(["/", "/signin", "/enroll", "/recover", "/link", "/open"]);

const csp =
  "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
  "frame-ancestors 'none'; form-action 'none'; base-uri 'none'";

export default {
  async fetch(req: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(req.url);
    try {
      if (url.origin !== env.RELAY_ORIGIN) throw new HttpError(421, "wrong host");
      const h = api[url.pathname];
      if (h) return await h(req, env, ctx);
      if (req.method !== "GET" && req.method !== "HEAD") throw new HttpError(405, "use GET");
      if (url.pathname === "/.well-known/jwks.json") return await machines.jwks(env);
      if (url.pathname === "/app.js") return withSecurity(new Response(script, { headers: { "Content-Type": "text/javascript; charset=utf-8" } }));
      if (url.pathname === "/app.css") return withSecurity(new Response(style, { headers: { "Content-Type": "text/css; charset=utf-8" } }));
      if (pages.has(url.pathname)) {
        // Pages are static: the script reads the URL and calls the API, so
        // no GET changes state.
        return withSecurity(new Response(page(url.pathname), { headers: { "Content-Type": "text/html; charset=utf-8" } }), {
          "Content-Security-Policy": csp,
        });
      }
      throw new HttpError(404, "not found");
    } catch (e) {
      return errorResponse(e);
    }
  },
} satisfies ExportedHandler<Env>;

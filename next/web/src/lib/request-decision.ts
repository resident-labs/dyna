import { call } from "./client.ts";
import type { OpDecideRequest } from "./contract.gen.ts";

// Both individual and group decisions pass through the same device-access boundary.
export async function requestDecision(admin: boolean, request: OpDecideRequest): Promise<void> {
  if (admin) await call("op.decide", request);
}

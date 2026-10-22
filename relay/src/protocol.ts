import { sha256 } from "./crypto";

// Wire fields shared with internal/connect. Fixed vectors live in testdata/connect.
export interface MachineInfo {
  id: string;
  name: string;
  owner: { id: string; name: string };
  epoch: number;
  status: string;
  app: string;
  cards: string;
  relay: string;
}

export interface MachineClaims {
  iss: string;
  aud: string;
  htm: string;
  htu: string;
  iat: number;
  exp: number;
  jti: string;
  bh: string;
}

export interface RelayPayload {
  op: "mint" | "revoke";
  sub?: string;
  dev?: string;
  epoch?: number;
  jkt?: string;
  origin?: string;
}

export interface RelayClaims extends RelayPayload {
  iss: string;
  aud: string;
  nonce: string;
  iat: number;
  exp: number;
}

export interface MintResponse {
  code?: string;
  sig?: string;
  error?: string;
}

export async function mintMessage(nonce: string, code: string): Promise<string> {
  return `dyna-connect-mint.v1\n${nonce}\n${await sha256(code)}`;
}

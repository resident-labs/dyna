import { call } from "./client.ts";
import type { TranscriptReadRequest } from "./contract.gen.ts";

// Shared API reads may still serve another view. Let the in-flight read finish, but do not
// consume it or schedule another page after this thread has gone away.
export function transcriptReader(active: () => boolean) {
  return async (request: TranscriptReadRequest) => {
    if (!active()) return undefined;
    const response = await call("transcript.read", request);
    return active() ? response : undefined;
  };
}

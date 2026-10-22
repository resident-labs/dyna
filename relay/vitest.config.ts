import { cloudflareTest } from "@cloudflare/vitest-plugin";
import { defineConfig } from "vitest/config";

// Test keys and settings. Nothing here is a real secret.
export const TEST_SIGNING_KEYS = JSON.stringify([
  {
    kty: "OKP",
    crv: "Ed25519",
    kid: "test-1",
    // RFC 8037 Appendix A test key.
    d: "nWGxne_9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2A",
    x: "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo",
  },
]);

export default defineConfig({
  plugins: [
    cloudflareTest({
      wrangler: { configPath: "./wrangler.jsonc" },
      miniflare: {
        bindings: {
          RELAY_ORIGIN: "https://connect.test",
          RP_ID: "connect.test",
          MACHINE_ZONE: "machines.test",
          MACHINE_URL: "https://m-{id}.machines.test",
          CF_API_BASE: "https://cf.test/client/v4",
          CF_ACCOUNT_ID: "acct",
          CF_ZONE_ID: "zone",
          CF_API_TOKEN: "test-token",
          RELAY_SIGNING_KEYS: TEST_SIGNING_KEYS,
          // sha256("bootstrap-token") in base64url
          BOOTSTRAP_INVITE_HASH: "xydzpN34HDrSuI_2LKL6mQeVFeyB8d8Z2cdwsBO3mns",
        },
      },
    }),
  ],
});

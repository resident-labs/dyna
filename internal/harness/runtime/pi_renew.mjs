// Renews one saved OAuth grant through Pi's own provider runtime.
//
// Dyna runs this with the Pi bundle's package. Pi resolves the provider's
// refresh hook and renews the credential under its own auth.json lock, exactly
// as it does for a turn, so Dyna never keeps a second copy of a provider's
// refresh protocol. The script prints one line and never a token.
//
// It calls ModelRuntime.getAuth, not ModelRegistry.getApiKeyForProvider: the
// registry swallows every authentication error and returns undefined, which
// would report a rejected grant as a missing credential.
//
// Output, on stderr, one line:
//   renewed
//   no-credential
//   error <status> <kind>
// status is the token endpoint's HTTP status when the cause carries one, else
// 0. kind is invalid_grant, network, timeout or unknown. Dyna decides whether a
// failure is a rejected grant or a transient one; this script reports the
// provider's own facts and ranks nothing itself.

import { join } from "node:path";
import { pathToFileURL } from "node:url";

const say = (word) => {
	process.stderr.write(word + "\n");
};

const [authPath, provider] = process.argv.slice(2);
const pkg = process.env.DYNA_PI_PACKAGE;
if (!authPath || !provider || !pkg) {
	say("error 0 unknown");
	process.exit(5);
}

// causeChain lists every error and cause through the wrapper Pi adds.
const causeChain = (error) => {
	const chain = [];
	let current = error;
	for (let depth = 0; current && depth < 8; depth++) {
		chain.push(current);
		current = typeof current === "object" ? current.cause : undefined;
	}
	return chain;
};

// statusOf reads an HTTP status from a cause or from the provider's own words.
const statusOf = (chain) => {
	for (const entry of chain) {
		const candidates = [entry?.status, entry?.statusCode, entry?.response?.status];
		for (const value of candidates) {
			if (typeof value === "number" && value >= 100 && value <= 599) {
				return value;
			}
		}
		const text = typeof entry?.message === "string" ? entry.message : "";
		const match = /\((\d{3})\)/u.exec(text);
		if (match) {
			return Number(match[1]);
		}
	}
	return 0;
};

// kindOf names a failure from the provider's explicit code, never from the
// generic wrapper Pi adds for every refresh failure.
const kindOf = (chain) => {
	for (const entry of chain) {
		const code = typeof entry?.code === "string" ? entry.code : "";
		const text = `${typeof entry?.message === "string" ? entry.message : ""} ${code}`;
		if (/invalid_grant|invalid_token|unauthorized/iu.test(text)) {
			return "invalid_grant";
		}
		if (/timeout|timed out|ETIMEDOUT|aborted/iu.test(text)) {
			return "timeout";
		}
		if (/fetch failed|ENOTFOUND|ECONNREFUSED|ECONNRESET|EAI_AGAIN|network/iu.test(text)) {
			return "network";
		}
	}
	return "unknown";
};

try {
	const mod = await import(pathToFileURL(join(pkg, "dist", "index.js")).href);
	const runtime = await mod.ModelRuntime.create({ authPath, refreshOnCreate: false, allowModelNetwork: false });
	const resolution = await runtime.getAuth(provider);
	if (!resolution || !resolution.auth || !resolution.auth.apiKey) {
		say("no-credential");
		process.exit(3);
	}
} catch (error) {
	const chain = causeChain(error);
	say(`error ${statusOf(chain)} ${kindOf(chain)}`);
	process.exit(5);
}
say("renewed");
process.exit(0);

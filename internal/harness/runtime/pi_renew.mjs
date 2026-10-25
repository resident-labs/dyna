// Renews one saved OAuth grant through Pi's own provider runtime.
//
// Dyna runs this with the Pi bundle's package. Pi resolves the provider's
// refresh hook and renews the credential under its own auth.json lock, exactly
// as it does for a turn, so Dyna never keeps a second copy of a provider's
// refresh protocol. The script prints one word to stderr and never a token.
//
// Exit codes: 0 renewed or still valid, 3 no signed-in credential, 4 the
// provider rejected the grant, 5 the provider runtime failed.

import { join } from "node:path";
import { pathToFileURL } from "node:url";

const fail = (code, word) => {
	process.stderr.write(word + "\n");
	process.exit(code);
};

const [authPath, provider] = process.argv.slice(2);
const pkg = process.env.DYNA_PI_PACKAGE;
if (!authPath || !provider || !pkg) {
	fail(5, "usage");
}

let runtime;
try {
	const mod = await import(pathToFileURL(join(pkg, "dist", "index.js")).href);
	runtime = await mod.ModelRuntime.create({ authPath, refreshOnCreate: false, allowModelNetwork: false });
	const key = await new mod.ModelRegistry(runtime).getApiKeyForProvider(provider);
	if (!key) {
		fail(3, "no-credential");
	}
} catch (error) {
	const cause = error && typeof error === "object" ? error.cause : undefined;
	const text = `${error?.message ?? error} ${cause?.message ?? ""}`;
	if (/invalid_grant|refresh.*(failed|rejected)/iu.test(text)) {
		fail(4, "grant-rejected");
	}
	fail(5, "refresh-failed");
}
process.exit(0);

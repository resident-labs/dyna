#!/usr/bin/env bash
# Build Dyna's Pi fork, pack one self-contained coding-agent tarball, and verify
# the tarball against the upstream npm package.
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: scripts/pi-pack.sh <output-tarball>" >&2
  exit 2
fi
repo_root=$(git rev-parse --show-toplevel)
vendor="$repo_root/third_party/pi"
# The version names one build: the fork level plus the first twelve hex
# characters of the third_party/pi tree hash, so two builds at one level never
# collide. The package manifest's dynaFork stays the level alone.
fork_tree=$(git -C "$repo_root" rev-parse "HEAD:third_party/pi")
version="1.4.0-dyna.6.${fork_tree:0:12}"
output=$1
if [[ "$output" != /* ]]; then
  output="$repo_root/$output"
fi
mkdir -p "$(dirname "$output")"
output="$(cd "$(dirname "$output")" && pwd)/$(basename "$output")"

scratch=${DYNA_SCRATCH:-${TMPDIR:-/tmp}}
mkdir -p "$scratch"
tmp=$(mktemp -d "$scratch/pi-pack.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

# CI installs and builds right before packing; PI_PACK_SKIP_BUILD=1 reuses that.
if [ "${PI_PACK_SKIP_BUILD:-0}" != "1" ]; then
  npm --prefix "$vendor" ci --ignore-scripts
  "$repo_root/scripts/pi-hydrate.sh"
  npm --prefix "$vendor" run build:offline
fi

stage="$tmp/package"
mkdir -p "$stage/node_modules/@earendil-works"
# npm ci leaves package-local node_modules in the workspace. Shipping them makes
# npm treat that partial tree as authoritative and skip installing real deps.
cp -a "$vendor/packages/coding-agent/." "$stage/"
rm -rf "$stage/node_modules"
cp "$vendor/LICENSE" "$stage/LICENSE"

# Bundle every internal runtime dependency, including telemetry, which is
# transitive through pi-ai. npm must never fetch Dyna's internal packages from
# the upstream registry. Third-party dependencies remain ordinary npm deps.
for entry in   agent:pi-agent-core   ai:pi-ai   codemode:pi-codemode   mcp:pi-mcp   telemetry:pi-telemetry   tui:pi-tui; do
  directory=${entry%%:*}
  package=${entry#*:}
  destination="$stage/node_modules/@earendil-works/$package"
  mkdir -p "$destination"
  cp -a "$vendor/packages/$directory/." "$destination/"
  rm -rf "$destination/node_modules"
  cp "$vendor/LICENSE" "$destination/LICENSE"
done

# npm treats bundleDependencies as complete: it never installs anything the
# bundled packages themselves need. Merge every bundled package's third-party
# dependencies into the staged manifest's dependencies, pinned to the exact
# version the vendored lockfile resolved, and fail on any version conflict.
node --input-type=module - "$stage" "$vendor" "$version" <<'EOF'
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const stage = process.argv[2];
const vendor = process.argv[3];
const version = process.argv[4];
const scope = "@earendil-works/";
const internal = ["pi-agent-core", "pi-ai", "pi-codemode", "pi-mcp", "pi-telemetry", "pi-tui"];
const lock = JSON.parse(readFileSync(join(vendor, "package-lock.json"), "utf8")).packages ?? {};

function lockVersion(name, dependent) {
  // Prefer the version the dependent itself resolves in the workspace: a root
  // entry can hold another consumer's older copy of the same package.
  const nestedFirst = dependent ? ["packages/" + dependent + "/node_modules/" + name, "node_modules/" + name] : ["node_modules/" + name];
  for (const key of nestedFirst) {
    if (lock[key]?.version) return lock[key].version;
  }
  const suffix = "node_modules/" + name;
  const nested = Object.keys(lock).filter((k) => k.endsWith("/" + suffix)).sort((a, b) => a.length - b.length);
  const found = nested.map((k) => lock[k]?.version).find(Boolean);
  if (!found) throw new Error("package-lock.json has no version for " + name);
  return found;
}

const bundledDirectories = { "pi-agent-core": "agent", "pi-ai": "ai", "pi-codemode": "codemode", "pi-mcp": "mcp", "pi-telemetry": "telemetry", "pi-tui": "tui" };
const required = {};
for (const name of internal) {
  const manifest = JSON.parse(readFileSync(join(stage, "node_modules", scope + name, "package.json"), "utf8"));
  for (const section of ["dependencies", "optionalDependencies"]) {
    for (const dep of Object.keys(manifest[section] ?? {})) {
      if (dep.startsWith(scope)) continue;
      const exact = lockVersion(dep, bundledDirectories[name]);
      if (required[dep] && required[dep] !== exact) {
        throw new Error("conflicting lockfile versions for " + dep + ": " + required[dep] + " and " + exact);
      }
      required[dep] = exact;
    }
  }
}

const files = ["package.json", ...internal.map((name) => join("node_modules", scope + name, "package.json"))];
for (const file of files) {
  const path = join(stage, file);
  const manifest = JSON.parse(readFileSync(path, "utf8"));
  manifest.version = version;
  manifest.dynaFork = 6;
  if (file !== "package.json") {
    // Match how upstream publishes its internal packages: no dependencies of
    // their own. npm treats bundleDependencies as complete and never installs
    // anything a bundled package declares, so everything they need is merged
    // into the top-level manifest instead.
    delete manifest.dependencies;
    delete manifest.optionalDependencies;
    delete manifest.peerDependencies;
    delete manifest.peerDependenciesMeta;
  } else {
    for (const section of ["dependencies", "optionalDependencies"]) {
      for (const [name, range] of Object.entries(manifest[section] ?? {})) {
        if (name.startsWith(scope) && range === "1.4.0") {
          manifest[section][name] = version;
        }
      }
    }
  }
  writeFileSync(path, JSON.stringify(manifest, null, 2) + "\n");
}

const path = join(stage, "package.json");
const manifest = JSON.parse(readFileSync(path, "utf8"));
manifest.dependencies ??= {};
for (const [name, exact] of Object.entries(required)) {
  const existing = manifest.dependencies[name] ?? manifest.optionalDependencies?.[name];
  if (existing !== undefined && existing !== exact && !/[~^><*| ]/.test(existing)) {
    throw new Error("cannot pin " + name + ": staged manifest wants " + existing + ", lockfile resolved " + exact);
  }
  manifest.dependencies[name] = exact;
}
// Every bundled package is also a declared dependency, so the logical install
// tree stays complete; npm satisfies these from the bundle, never the registry.
for (const name of internal) {
  manifest.dependencies[scope + name] ??= version;
}
manifest.bundleDependencies = internal.map((name) => scope + name).sort();
writeFileSync(path, JSON.stringify(manifest, null, 2) + "\n");
console.log("merged " + Object.keys(required).length + " third-party dependencies from bundled packages");
EOF

packed=$(npm pack "$stage" --ignore-scripts --pack-destination "$tmp" --silent)
mv "$tmp/$packed" "$output"
integrity=$(node -e 'const crypto=require("crypto"),fs=require("fs");process.stdout.write("sha512-"+crypto.createHash("sha512").update(fs.readFileSync(process.argv[1])).digest("base64"))' "$output")
printf 'Pi fork tarball: %s\nIntegrity: %s\n' "$output" "$integrity"

# Verify the packed tarball the way Dyna installs it, then compare the installed
# package set with the upstream npm package. This catches anything the tarball
# silently drops, such as the quickjs wasm codemode needs.
base=$(node -e 'process.stdout.write(require(process.argv[1] + "/package.json").version)' "$vendor/packages/coding-agent")
fork_prefix="$tmp/verify-fork"
npm_prefix="$tmp/verify-npm"
# One retry: the registry CDN intermittently 404s a tarball that exists.
verify_install() {
  npm install --global --prefix "$1" --package-lock --ignore-scripts "$2" ||
    npm install --global --prefix "$1" --package-lock --ignore-scripts "$2"
}
verify_install "$fork_prefix" "$output"
verify_install "$npm_prefix" "@earendil-works/pi-coding-agent@$base"
node --input-type=module - "$fork_prefix" "$npm_prefix" "$version" <<'EOF'
import { execFileSync, spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

function installedNames(prefix) {
  // npm ls exits nonzero when the tree has problems; the JSON is still complete.
  const run = spawnSync("npm", ["ls", "-g", "--prefix", prefix, "--all", "--json"], { encoding: "utf8", maxBuffer: 1 << 26 });
  const tree = JSON.parse(run.stdout || "{}");
  const names = new Set();
  (function walk(node) {
    for (const name of Object.keys(node.dependencies ?? {})) {
      names.add(name);
      walk(node.dependencies[name]);
    }
  })(tree);
  return names;
}

const fork = installedNames(process.argv[2]);
const upstream = installedNames(process.argv[3]);
const missing = [...upstream].filter((name) => !fork.has(name));
if (missing.length > 0) {
  console.error("packages missing from the fork install: " + missing.join(", "));
  process.exit(1);
}

const version = process.argv[4];
const bin = execFileSync(process.argv[2] + "/bin/pi", ["--version"], { encoding: "utf8" }).trim();
if (bin !== version) {
  console.error("packed pi --version printed " + bin + ", expected " + version);
  process.exit(1);
}

// Dyna's model discovery reads this exact offline output.
const listed = spawnSync(process.argv[2] + "/bin/pi", ["--offline", "--list-models", "--json"], { encoding: "utf8", maxBuffer: 1 << 24 });
if (listed.status !== 0) {
  console.error("pi --list-models --json exited with " + listed.status);
  process.exit(1);
}
const entries = JSON.parse(listed.stdout);
if (!Array.isArray(entries)) {
  console.error("pi --list-models --json did not print a JSON array");
  process.exit(1);
}
// With no signed-in provider the catalog is empty; [] is the contract.
if (entries.length === 0) {
  console.log("pi --list-models --json printed an empty catalog (no provider credentials)");
}
for (const entry of entries) {
  if (
    typeof entry.provider !== "string" ||
    typeof entry.id !== "string" ||
    typeof entry.name !== "string" ||
    typeof entry.contextWindow !== "number" ||
    typeof entry.maxTokens !== "number" ||
    typeof entry.reasoning !== "boolean" ||
    !Array.isArray(entry.thinkingLevels) ||
    entry.thinkingLevels.some((level) => typeof level !== "string")
  ) {
    console.error("pi --list-models --json printed a malformed entry: " + JSON.stringify(entry));
    process.exit(1);
  }
}

// One codemode script with no network: this is the exact path that broke when
// the tarball dropped quickjs-wasi.
const codemode = pathToFileURL(process.argv[2] + "/lib/node_modules/@earendil-works/pi-coding-agent/node_modules/@earendil-works/pi-codemode/dist/index.js").href;
const { InlineCodemodeSandbox, loadQuickJSWasm } = await import(codemode);
const sandbox = new InlineCodemodeSandbox({ wasm: loadQuickJSWasm() });
try {
  const result = await sandbox.execute("return 'fork-' + 6 * 7");
  if (!result.ok || result.value !== "fork-42") {
    console.error("codemode script failed: " + JSON.stringify(result));
    process.exit(1);
  }
} finally {
  await sandbox.close();
}
console.log("verified " + fork.size + " packages, pi --version " + version + ", codemode ok");
EOF

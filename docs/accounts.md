# Private harness accounts

Dyna runs every catalog harness on its private copy, with its own saved accounts. Each harness has one active account. The owner's own harness login stays separate.

## Sign in and switch

Open **Settings → Harnesses** to manage accounts:

- **Sign in** adds an account. If Dyna finds your personal login for that harness, you can copy it once instead of signing in again.
- **Switch** makes another saved account active. A switch blocks new turns and waits for running turns to finish.
- **Remove** deletes a saved account after confirmation.

The CLI does the same with `dyna harness login <harness>` and `dyna harness accounts activate <account-id>`. See [private harness runtimes](private-harness-runtimes.md) for homes and sign-in details.

## Accounts by provider

One account is one provider sign-in: anthropic, chatgpt, kimi-code, xai, meta-muse, opencode-go, deepseek, zai. A harness only runs the account, and two accounts of one provider can run at the same time because each keeps its own home.

- `dyna accounts list [--provider P] [--room R] [--refresh] [--json]` lists the accounts grouped by provider, with the sign-in state and the usage windows. `--refresh` asks each provider for current usage.
- `dyna accounts use <account> [--room R | --global]` chooses the account its provider runs. The default is global. `--room` changes only that room: its turns use the chosen account and every other room keeps its own.
- `dyna accounts add <provider> [--label L] [--api-key-stdin]` adds an account. A key is read from standard input only, never from an argument. Without `--api-key-stdin` the command prints the sign-in step to run.
- `dyna accounts add --import <harness> [--account A] [--dry-run]` moves the saved sign-ins of one native harness into the Pi accounts of their providers, then retires the source, so Dyna never keeps two saved copies that renew one refresh token. A native CLI that the owner still runs outside Dyna keeps its own login. `--dry-run` lists the move and writes nothing. Dyna refuses a harness that an enabled profile or an open session still runs, and names them.
- `dyna accounts remove <account>` deletes an account and its homes.

An account is named by ID, an ID prefix, or a label that names one account only. A saved row Dyna cannot read shows as a skipped row with its reason; it never hides the other accounts. A harness that keeps one shared sign-in has a global account only: a room cannot choose its own account for it.

`dyna usage` was removed. Use `dyna accounts list --refresh` instead.

## Usage

**Settings → Accounts** lists the private accounts with their plan and usage windows. It first shows the last saved observation, with its checked time. **Refresh** asks each provider for the active account's current usage. A window whose reset time has passed shows reset usage. Missing quota stays unknown; it is never zero or unlimited.

Pi keeps one account for each provider. A usage reader runs for the provider, not the harness, and reads the credential from that account's own Pi home: the ChatGPT plan, Kimi Code, xAI Grok credits, OpenCode Go, the DeepSeek balance, and the z.ai Coding Plan. Muse usage needs a Muse sign-in through Pi, which keeps the identity token; a Muse API key cannot read it. An Anthropic API key publishes no usage Dyna can read. Those accounts show that typed reason. A reader that returns the account's credential, or a value that looks like a key or token, has its result rejected.

Dyna never uses a Claude Pro/Max sign-in through Pi. Anthropic bills a Claude Pro/Max sign-in used by a third-party harness as extra per-token usage. Use the Claude Code harness, or add an Anthropic API key in Pi.

**Sign in** on a Usage row opens the Harnesses page. **Create profile** in an account's details starts a profile for that account's harness.

Dyna renews private credentials in the background while the host runs. If a provider rejects a login, the account shows **Needs sign-in**.

## Storage

Dyna stores the account index in `dyna.db`. Each account's login files live in its own native home under `$XDG_DATA_HOME/dyna/accounts`, or `~/.local/share/dyna/accounts`. Files have mode `0600` and directories have mode `0700`. Credentials stay on the host. Browser and mobile clients receive account projections only.

To copy your personal login into Dyna, run `dyna harness login <harness> --use-personal`. It works only for a harness with no saved account. If an account with another identity exists, sign in instead.

# Private harness accounts

Dyna runs every catalog harness on its private copy, with its own saved accounts. Each harness has one active account. The owner's own harness login stays separate.

## Sign in and switch

Open **Settings → Harnesses** to manage accounts:

- **Sign in** adds an account. If Dyna finds your personal login for that harness, you can copy it once instead of signing in again.
- **Switch** makes another saved account active. A switch blocks new turns and waits for running turns to finish.
- **Remove** deletes a saved account after confirmation.

The CLI does the same with `dyna harness login <harness>` and `dyna harness accounts activate <account-id>`. See [private harness runtimes](private-harness-runtimes.md) for homes, sign-in details and the Antigravity keyring limit.

## Usage

**Settings → Usage** lists the private accounts with their plan and usage windows. It first shows the last saved observation, with its checked time. **Refresh** asks each provider for the active account's current usage. A window whose reset time has passed shows reset usage. Missing quota stays unknown; it is never zero or unlimited.

**Sign in** on a Usage row opens the Harnesses page. **Create profile** in an account's details starts a profile for that account's harness.

Dyna renews private credentials in the background while the host runs. If a provider rejects a login, the account shows **Needs sign-in**.

## Storage

Dyna stores the account index in `dyna.db`. Each account's login files live in its own native home under `$XDG_DATA_HOME/dyna/accounts`, or `~/.local/share/dyna/accounts`. Files have mode `0600` and directories have mode `0700`. Credentials stay on the host. Browser and mobile clients receive account projections only.

To copy your personal login into Dyna, run `dyna harness login <harness> --use-personal`. It works only for a harness with no saved account. If an account with another identity exists, sign in instead.

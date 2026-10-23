# Managed Linux install

A managed install runs one Dyna environment in one systemd-nspawn container.
The container runs the web server, agents, and agent computers.
The host reaches the web server through loopback.
Use one managed environment per machine.
On Windows, use the [WSL2 install](windows-install.md) instead.

The installer is part of the Go CLI. It uses the existing Cobra command structure.
It runs as your normal user and calls sudo for each system step.
It prints each sudo command before it runs it.
It does not change the host Dyna binary, data, or user service.

## Requirements

Use Linux with systemd, systemd-nspawn, machinectl, sudo, and flock.
Recent systemd releases also need importctl.
On Arch Linux, install the container tools with:

```sh
sudo pacman -S systemd
```

Use a trusted Dyna rootfs tar, such as `dyna.tar.zst`.
Build it with `image/build.sh`; see [the image README](../image/README.md).
`dyna.tar.zst` is 706,590,233 bytes. The unpacked tar is 2,689,075,200 bytes.
The image keeps the Codex and Claude CLIs. An image under 600 MB needs
these CLIs to install on first use instead.
The installer accepts a local file or an HTTPS URL.
HTTPS redirects must also use HTTPS.
The tar runs code with root authority during setup.
Check its source before you install it.
The installer verifies SHA-256 before it imports an image.
For an HTTPS URL, pass `--sha256 <64 hexadecimal digits>` or publish a
checksum at `<image URL>.sha256`. The checksum file must use the `sha256sum`
format and name the image file, as `image/build.sh` produces.
The installer fetches that file when `--sha256` is absent.
A missing checksum or a mismatch stops the install. It deletes a failed download.
A local image without `--sha256` prints a warning and remains usable for development.
Checksum verification does not verify a release signature.

The image must contain user `dyna`, its home at `/home/dyna`,
`/usr/local/bin/dyna`, `/usr/local/bin/dyna-desktop`,
`/etc/dyna/image-release`, and `/etc/systemd/user/dyna-engine.service`.
The image must include the drivers and tools that agent computers need.
See [Agent computers](computers.md).

## Install

Run this as your normal user:

```sh
dyna install --managed --image /path/to/dyna.tar.zst
```

The defaults are container name `dyna`, code folder `~/code`,
web port `7467`, and card port `7468`.
An explicit setup can use these options:

```sh
dyna install --managed \
  --image https://example.com/releases/dyna.tar.zst \
  --sha256 "<published-sha256>" \
  --name dyna \
  --code "$HOME/code" \
  --port 7467 --card-port 7468
```

The installer checks that both loopback ports are free.
It creates the code folder if needed.
It refuses an existing image or configuration with the same name.
It imports the tar with `importctl import-tar --class=machine`.
On older systems without importctl, it uses `machinectl import-tar`.

The installer writes these host files:

- `/etc/systemd/nspawn/<name>.nspawn`
- `/etc/systemd/system/systemd-nspawn@<name>.service.d/dyna.conf`
- `/etc/systemd/nspawn/.dyna-machines-target`, when Dyna enables the host target

The container boots with `Boot=yes` and `PrivateUsers=no`.
It shares the host network through `VirtualEthernet=no`.
Both web listeners stay on loopback.
The code folder has the same absolute path on both sides.
A new `/home/dyna/code` symlink points to that folder.
The path cannot end with a space.
It cannot contain a colon, quote, backslash, percent sign, or control character.
System paths and paths inside `/home/dyna` are refused.

The installer sets the container user's numeric user ID and primary group ID
to match the host user.
The default image IDs are both 1000.
It changes ownership only inside the container home before the code folder is mounted.
If the requested ID belongs to another image account or group, setup fails.
Use a compatible image; do not change ownership of the host code to work around it.
The host user must have a nonzero user ID.

When `/dev/dri` exists, the installer binds it into the container.
It adds `DeviceAllow=char-drm rw` to the container service.
Unix device permissions still apply. Check the container user's render or video
group membership if hardware rendering fails.

The container service has these limits:

- `CPUWeight=20`
- `MemoryHigh=65%`
- `MemoryMax=80%`

Inside the container, the installer writes the selected ports to
`/etc/dyna/serve.env`.
It enables linger and the `dyna-engine.service` user unit for `dyna`.
It then enables and starts the container with machinectl.
Startup is asynchronous. Use the checks below if the page is not ready.

## Open the app

Open `http://127.0.0.1:7467` in the host browser.
Get a pairing code with:

```sh
sudo machinectl shell dyna@dyna /usr/local/bin/dyna engine run --pair
```

Use your chosen container name in place of the second `dyna`.
Pairing codes grant owner access. Do not share them in agent posts or logs.

The host desktop app cannot attach to this server yet.
Its native window binds its own local Go backend.
It has no remote backend option.
Use the host browser for the managed environment.
Resident Labs Connect is another route to the container's server.
Configure Connect inside the container; see [Connect](desktop/CONNECT.md).
Do not start another host backend for the managed environment.

## Install software inside Dyna

The Dyna environment is a normal Arch Linux machine. Agents and people can
install what they need, and installed software stays across restarts.

- The image is built from a dated Arch archive snapshot, so builds repeat
  exactly. The installed system does not stay on that snapshot.
- On first boot, `dyna-pacman-update.service` runs `pacman -Syu` once against
  the live mirrors in `/etc/pacman.d/mirrorlist`. After that, the system is
  current, and `/var/lib/dyna/pacman-updated` records that it ran.
- Install packages with `sudo pacman -S <package>`. The user has passwordless
  sudo. `npm`, `pip` and other tools work as usual.
- As on any Arch machine, run `sudo pacman -Syu` before you install if the
  system has not been updated for a while. This avoids a partial upgrade.
- Updating Dyna replaces only Dyna's own programs. It never replaces the system
  or removes what you installed.

## Agent profiles

The image has no Dyna profiles. After the first login, add the profiles you
use inside the container:

```sh
sudo machinectl shell dyna@dyna /usr/local/bin/dyna profiles add \
  --name my-builder --harness codex --model <model-id> --taste 7 --intelligence 9 --cost 6
```

You can also add profiles in the app under Settings > Profiles. Dyna creates
no profiles for you.

The profiles use Dyna's private copy of the CLI and Dyna's saved accounts.

## Agent credentials

The image does not contain Claude Code or Codex. Dyna installs a CLI the first
time sign-in or a session needs it. The install needs internet access and can
take a minute. The Harnesses page shows "Installing…" while it runs. See
[first-use install](private-harness-runtimes.md#first-use-install).

Sign in from the Harnesses page, or open a shell and sign in through Dyna:

```sh
sudo machinectl shell dyna@dyna
dyna harness login codex
```

You can opt in to a host credential bind by editing the container's
`.nspawn` file. For example, add this under `[Files]`:

```ini
Bind=/home/alice/.codex:/home/dyna/.codex
```

Replace the host path with your own path.
Restart the container after a bind change.
A writable bind lets the agent read and change those credentials.
A read-only bind can break token refresh.
The installer does not copy or bind credentials by default.
Keep credentials outside the repository.

## Check the user manager and limits

```sh
sudo machinectl status dyna
sudo machinectl shell dyna@dyna /usr/bin/systemctl --user status dyna-engine.service
sudo machinectl shell dyna@dyna /usr/bin/systemd-run --user --scope /usr/bin/true
sudo systemctl show systemd-nspawn@dyna.service -p CPUWeight -p MemoryHigh -p MemoryMax
```

Linger starts the container user manager at boot.
Agent commands use `systemd-run --user` when that manager and command are available.
The existing agentslice code starts commands directly when either is missing.
The host container limits still apply.
See [Resource limits](resource-limits.md).

For server logs, run:

```sh
sudo machinectl shell dyna@dyna /usr/bin/journalctl --user -u dyna-engine.service
```

## Uninstall

This command deletes the container and all data stored inside it:

```sh
dyna uninstall --managed
```

Use `--name <name>` for a custom name.
The installer stops and disables the container, removes its image,
and removes the two managed configuration files.
It checks the Dyna marker before removing an environment.
Dyna records whether it enabled `machines.target` during install.
Uninstall disables that target only if Dyna enabled it and no other machine
is running or enabled under the target. It keeps the record while another
machine needs the target. It preserves a target that was already enabled.
Older installs without this record leave the target enabled.
On Btrfs, uninstall can take a few minutes while the subvolume is deleted.
It never deletes or changes the bound code folder or a bound credential folder.
Back up internal Dyna history and credentials before uninstall.
There is no keep-data option in this version.

A failed install can leave an imported image and marked configuration.
Run uninstall with the same name before retrying.
If the first configuration write failed, remove the imported image with
`sudo machinectl remove <name>` after checking its name.

## Measured results

These numbers come from one laptop (8 cores, 30 GiB RAM, Btrfs, Intel GPU)
with image 0.1.0. The image was 717.9 MiB compressed.

| Check | Result |
| --- | --- |
| Install, including import | 11.3 seconds |
| Container start until the web app answers | 2.1 seconds first, 1.6 seconds later |
| Idle container memory after 2 minutes | 63.2 MiB total, 27.1 MiB anonymous |
| Agent computer renderer | GLES2 on `/dev/dri/renderD128` |
| Live view stream encoder | software |
| Uninstall on Btrfs | 159 seconds |

One Codex agent session ran in the container. It opened `foot` on its
computer and typed text. A browser on the host watched that computer live
through `http://127.0.0.1:<port>`.

`machinectl enable` also enables `machines.target` on the host.
Dyna now tracks and removes its target enablement when no other machine needs it.

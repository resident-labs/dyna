# Windows install with WSL2

On Windows, Dyna runs as one WSL2 distro named `dyna`.
The distro runs the web server, agents, and agent computers.
A browser on Windows opens the web server through `http://localhost:7467`.
Use one Dyna environment per machine.

The installer is a PowerShell script:
[scripts/windows/install-dyna.ps1](../scripts/windows/install-dyna.ps1).
It imports the same rootfs tar as the [managed Linux install](managed-install.md).
It runs as your normal Windows user. It does not need administrator rights.
It does not change `.wslconfig` or any other distro.

## Requirements

Use Windows 10 or 11 with WSL 2 from the Microsoft Store.
`wsl --version` must work and report version 2 or later.
If it fails, run `wsl --install --no-distribution` or `wsl --update`.
The installer was tested with WSL 2.7.3.

Use a trusted Dyna rootfs tar, such as `dyna.tar.zst`.
Build it with `image/build.sh`; see [the image README](../image/README.md).
The installer accepts a local `.tar`, `.tar.xz`, `.tar.zst`, `.tar.gz` or an HTTPS URL.
`wsl --import` reads compressed tar files directly.
WSL 2.7.3.0 imported and booted the `dyna.tar.zst` image in about 6 seconds.
That test also confirmed systemd, the pacman database and keyring,
and the computer tools.
If import fails on a compressed file, run `wsl --update` or use `dyna.tar`.
The tar runs code with root authority inside WSL.
Check its source and release checksum before you install it.
The installer does not verify a release signature or checksum.

`dyna.tar.zst` is 706,590,233 bytes. The unpacked tar is 2,689,075,200 bytes.
The image needs about 3 GB of disk after import.

## Install

Run this in PowerShell as your normal user:

```powershell
powershell -ExecutionPolicy Bypass -File install-dyna.ps1 -Image C:\Downloads\dyna.tar.zst
```

The defaults are distro name `dyna`, folder `%LOCALAPPDATA%\Dyna\wsl\dyna`,
web port `7467`, card port `7468`, and Linux user ID `7467`.
An explicit setup can use these options:

```powershell
powershell -ExecutionPolicy Bypass -File install-dyna.ps1 `
  -Image https://example.com/releases/dyna.tar.zst `
  -Name dyna -InstallDir D:\WSL\dyna `
  -Port 7467 -CardPort 7468 -Uid 7467
```

The installer does these steps:

1. It checks `wsl --version`.
2. It refuses an existing distro with the same name or a folder that is not empty.
3. It checks that both ports are free on Windows and inside WSL.
4. It downloads an HTTPS image with `curl.exe`, if needed. Redirects must also use HTTPS.
5. It runs `wsl --import <name> <dir> <image> --version 2`.
6. It waits for systemd. The image's `/etc/wsl.conf` turns systemd on.
7. It checks the Dyna files and sets the `dyna` user ID.
8. It writes the ports to `/etc/dyna/serve.env`.
9. It enables linger and the `dyna-engine.service` user unit.
10. It restarts the distro and starts a keepalive process.
11. It waits for HTTP 200 from `http://localhost:<port>`.

It prints how to open the app and how to get a pairing code.
A failed install prints the uninstall command. Run it before you retry.

### Why the user ID is not 1000

All WSL2 distros share one Linux kernel and one cgroup tree.
Most distros give their first user ID 1000.
Two systemd distros with the same user ID both use `user@1000.service`.
The second one fails with "Device or resource busy".
So the installer gives `dyna` user ID 7467.
Use `-Uid` to choose another free ID from 1001 to 59999.
The installer refuses an ID that another running distro uses.

### Keep the distro running

WSL stops an idle distro a few seconds after its last Windows process exits.
This happens even with systemd. It would stop `dyna-engine`.
The installer starts `conhost.exe --headless wsl.exe -d <name> --exec /bin/sleep infinity`.
This process has no window. It keeps the distro running.
The installer also registers a scheduled task named `Dyna WSL (<name>)`.
The task runs the same command when you log on to Windows.
Pass `-NoAutoStart` to skip the task. Then Dyna stops after you log off,
and starts again the next time you run any `wsl -d <name>` command
that stays open.

## Network

The web server listens on `127.0.0.1` inside WSL.
The default WSL NAT mode forwards Windows `localhost` to it.
Mirrored networking is not needed.
The installer never changes `.wslconfig`, because that file applies to every distro.

All WSL2 distros share one network namespace.
A port that Dyna uses is also busy in your other distros.
Pick other ports with `-Port` and `-CardPort` if you need them elsewhere.

## Open the app

Open `http://localhost:7467` in a Windows browser.
Get a pairing code with:

```powershell
wsl -d dyna -- dyna engine run --pair
```

Use your chosen distro name in place of `dyna`.
Pairing codes grant owner access. Do not share them in agent posts or logs.
The desktop app cannot attach to this server yet. Use the browser.

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

## Agent profiles and credentials

The image has no Dyna profiles, no credentials and no agent CLIs.
Open a shell in the distro and sign in to each agent tool through Dyna:

```powershell
wsl -d dyna
dyna harness login codex
dyna profiles add --name my-builder --harness codex --model <model-id> --taste 7 --intelligence 9 --cost 6
```

You can also add profiles in the app under Settings > Profiles.

The first sign-in installs Dyna's private copy of the CLI. It needs internet
access and can take a minute. Accounts stay under Dyna's data folder.

`wsl -d dyna` opens a shell as the `dyna` user.
Credentials stay inside the distro. Uninstall deletes them.

## Changes for WSL2 in the image

The image has these changes for WSL2. They are safe under systemd-nspawn too.

- `/etc/wsl.conf` sets `appendWindowsPath=false`.
  Windows folders under `/mnt/c` are not in `PATH`.
  Command lookups stay fast, and agents do not find Windows tools by accident.
  Windows programs can still run by full path, such as `/mnt/c/Windows/System32/cmd.exe`.
- `systemd-binfmt.service` is masked.
  binfmt_misc entries are global to the WSL2 kernel.
  This service would replace the entries of every distro, including `WSLInterop`.
- `dyna-wsl.service` runs `/usr/local/libexec/dyna-wsl-setup` at boot, only on WSL.
  It unmounts `/proc/sys/fs/binfmt_misc` in this distro.
  Without that, systemd shutdown clears every binfmt entry,
  and Windows interop breaks in all distros until they restart.
- The same script covers the read-only WSLg `/tmp/.X11-unix` with a writable tmpfs.
  Agent computers create their X11 sockets there.
  The WSLg socket `:0` is then not available in this distro. Agents use their own computers.
- The image no longer enables linger. Each installer enables it
  after it sets the final user ID.

## Agent computers and the GPU

Agent computers use software rendering on WSL2.
WSL2 has no DRM render node (`/dev/dri`). It has `/dev/dxg` for Direct3D 12.
labwc needs a DRM render node for GLES2, so it uses its pixman renderer.
Mesa in the image includes the `d3d12` driver, but Dyna computers do not use it.
The live view stream uses the software encoder.

Keep code in the Linux file system, such as `/home/dyna/code`.
Files under `/mnt/c` work, but they are much slower.

## Memory and CPU

All distros share the WSL2 VM. `.wslconfig` sets its memory size.
The installer adds limits for the Dyna user slice, as the nspawn install does:
`CPUWeight=20`, `MemoryHigh=65%` and `MemoryMax=80%` of the VM memory.
See `/etc/systemd/system/user-<uid>.slice.d/dyna.conf`.

## Check the distro

```powershell
wsl -l -v
wsl -d dyna -- systemctl is-system-running
wsl -d dyna -- systemctl --user status dyna-engine.service
wsl -d dyna -- journalctl --user -u dyna-engine.service
```

## Uninstall

This command deletes the distro and all data stored inside it:

```powershell
powershell -ExecutionPolicy Bypass -File install-dyna.ps1 -Uninstall
```

Use `-Name` and `-InstallDir` for a custom setup.
The installer checks the Dyna marker `/etc/dyna/image-release` first.
It refuses a distro without it.
It removes the scheduled task, runs `wsl --unregister <name>`,
and removes the install folder if it is empty.
Back up Dyna history and credentials before uninstall.

## Known limits

- Other systemd distros can still clash with Dyna on shared system unit names,
  because all distros share one cgroup tree. This is a WSL limit.
- Each systemd distro that shuts down with binfmt_misc mounted clears the
  interop entries of all distros. Dyna avoids this. Other distros can still do it.
  Restart the affected distro, or run `sudo systemctl restart systemd-binfmt` in it,
  if `.exe` files fail with "exec format error".
- The scheduled task needs a Windows logon. Dyna does not start before logon.
- Windows on Arm is not supported. The image is x86_64.

## Measured results

These numbers come from one desktop (16 cores, Windows 10.0.26200, WSL 2.7.3,
kernel 6.6.114.1, `.wslconfig` memory 24 GB) with image 0.1.0.
The image was 717.5 MiB compressed. The test used distro name `dyna-test`
and ports 17467 and 17468.

| Check | Result |
| --- | --- |
| Whole installer run, including import | 13.9 seconds |
| `wsl --import` of `dyna.tar.zst` | 5.6 seconds |
| Distro start until `http://localhost:<port>` answers | 2.3 seconds |
| Idle memory after 2 minutes, Dyna user slice | 50.7 MiB total, 19.1 MiB anonymous |
| Idle memory after 2 minutes, all 38 distro processes | 264.6 MiB summed RSS |
| Disk use after import | 3.0 GB |
| Agent computer start | 125 milliseconds |
| Agent computer renderer | pixman (software); GLES2 fails with no DRM device |
| Live view stream encoder | software |
| Uninstall | 2.1 seconds |

One Codex agent session ran in the distro. It opened `foot` on its
computer and typed text. A headless Chromium watched that computer live
through the web app.

# Managed Linux image

This directory builds an x86_64 Arch Linux root filesystem. It runs one Dyna
environment under systemd-nspawn or WSL2. The archive contains systemd, Dyna,
the web service, Chromium, and the computer runtime.

The archive does not contain Claude Code. Dyna installs a harness CLI
the first time sign-in or a session needs it. See
[first-use install](../docs/private-harness-runtimes.md#first-use-install).
The image keeps nodejs and npm because that installer runs npm.

The build uses pacstrap. It does not use Docker. The host must run Arch Linux
and have passwordless sudo for package installation and systemd-nspawn.
Install the build tools:

```sh
sudo pacman -S --needed arch-install-scripts zstd nodejs npm gcc pkgconf gtk3 webkit2gtk-4.1 curl gnupg
```

Use the Go version in go.mod. Build from a clean checkout:

```sh
image/build.sh /path/outside/checkout/dyna-image
```

The output directory must be new. After an interrupted package download,
pass --resume-download to reuse its cache before any package was installed. The
script writes dyna.tar, dyna.tar.zst, its SHA-256 checksum, packages.lock,
package-sizes.txt, and build-time.txt. The rootfs directory remains for inspection.
The runner needs at least 15 GB of free disk space. The workflow runs only
through workflow_dispatch and uploads the compressed archive and package list.
Zstandard uses two compression threads.

## Package and source pins

versions.env pins the Arch Linux Archive date.
pacman checks package signatures using the host keyring. The archive date
pins the build repository state. Finalization switches the image to live mirrors.
The image pins no agent versions. The first-use install picks the version.

Dyna and dyna-desktop build on the host from this checkout. This avoids
shipping a Go compiler and frontend build tools in the image. The desktop
build uses the same command as desktop/build.sh. The host Go, Node.js, npm,
GTK and WebKitGTK versions also affect the result. Use the same host toolchain
for repeated builds. SOURCE_DATE_EPOCH defaults to the commit time and sets
the image build date and tar timestamps. This is a repeatable package recipe;
it does not yet promise byte-identical archives across host toolchains.

## Runtime contract

The dyna user has uid 1000, gid 1000, home /home/dyna, and passwordless sudo.
The installers set the final IDs and enable linger. Dyna and dyna-desktop are in /usr/local/bin.
The image uses the shared unit writer for /etc/systemd/user/dyna-engine.service
and dyna-computers.service without enabling either unit. A port drop-in configures
the engine.
The service defaults to loopback ports 7467 and 7468. The installer can
override DYNA_SERVE_PORT and DYNA_CARD_PORT in /etc/dyna/serve.env.

The image includes /etc/wsl.conf with systemd enabled, dyna as the default
user, and no Windows PATH. systemd-binfmt is masked, and dyna-wsl.service
adapts the distro to the WSL2 kernel that all distros share. /etc/dyna/image-release records version, build_date, archive_date,
and git_commit. The archive has an empty machine-id, no SSH host keys, and
no credentials. A first boot unit creates a local pacman keyring from the
shipped Arch public keys. finalize.sh points
`/etc/pacman.conf` at the live mirrors after the build. A second first boot
unit, `dyna-pacman-update.service`, then runs `pacman -Syu` once, so the
installed system leaves the build snapshot. The archive date pins only the
build. No pacman private key is shared by images.
Only the SSH client is needed; sshd is not enabled.

The computer runtime includes labwc, wayvnc, Xwayland, grim, wtype,
wlr-randr, foot and setpriv. Mesa includes software rendering and Intel/AMD
OpenGL drivers. Vulkan driver packages are omitted; labwc uses GLES2 and Mesa supplies llvmpipe. NVIDIA is out of scope.
FFmpeg comes through runtime dependencies. Optional VA-API driver packages
are omitted to reduce size. The runtime can use its normal VNC encodings.

Import on older systemd hosts with machinectl import-tar. New systemd
versions move that command to importctl:

```sh
sudo importctl --class=machine import-tar /path/to/dyna.tar.zst dyna
sudo machinectl start dyna
```

The [systemd importctl reference](https://github.com/systemd/systemd/blob/main/man/importctl.xml)
lists zstd as a supported tar compression format.

For WSL2, use scripts/windows/install-dyna.ps1. It runs wsl --import with
dyna.tar.zst or dyna.tar. See [the Windows install](../docs/windows-install.md).

The installer must pass /dev/dri for GPU access. The computer runtime uses
its software fallback when no render node works.

Run the smoke test after the build:

```sh
image/smoke.sh /path/outside/checkout/dyna-image
```

It extracts the compressed artifact into /var/lib/machines/dyna-imgtest,
then boots it as dyna-imgtest,
checks systemd, the user manager, the command versions, headless Chromium,
the software labwc renderer with a grim screenshot, and an HTTP 200 response
from dyna engine run on test ports,
then terminates the container and removes its test image.
The normal machine directory permits setuid programs such as sudo.
A root filesystem under /tmp can inherit the host nosuid mount option.
Chromium uses --no-sandbox only for this boot smoke test.
Pass --uncompressed to test dyna.tar while compression runs.
The script leaves boot.log and smoke.log in the output directory.
Both scripts record host sudo commands in sudo-commands.log.
Do not run this test if another machine named dyna-imgtest exists.

## Known archive defect

The 2026/10/15 archive database pins aml-1.0.0-1 with SHA-256
d3a87fac8acf0b557f408a6911cabc2385b54275f7b81049fc0227e4af8a13cb.
The archive URL instead serves an older build under the same filename.
2026/10/14 and 2026/10/16 have the same mismatch.

The recipe has one explicit recovery URL for this package in versions.env.
It downloads the database-pinned bytes from the official Arch mirror,
checks the fixed SHA-256 and Arch signature, and seeds pacman's cache.
Pacman then checks its archive database signature too. Other packages use
the archive only. The live mirror will drop this file when aml updates.
A later rebuild will then fail and need a new pin or a fixed archive date.
Signature and checksum checks must remain enabled.


The image leaves systemd-networkd and systemd-resolved disabled.
The managed container shares the host network. nspawn supplies its DNS file,
and WSL generates its own. The archived /etc/resolv.conf is a plain empty file.

To apply the finalization steps and repack an existing rootfs from this recipe,
use image/build.sh OUTPUT_DIRECTORY --finalize-only. This skips source and
package installation, updates the overlay and release metadata, removes
unused packages, and recreates the plain and Zstandard archives.


## Measured size and remaining work

The image no longer contains Claude Code. Dyna installs it on
first use. One full build ran with nice -n 19 and Zstandard. A
--finalize-only repack then applied the content cuts below.

| Artifact | With agent CLIs, bytes | Without agent CLIs, bytes |
| --- | ---: | ---: |
| Shipped dyna.tar.zst | 706,590,233 | 516,028,965 |
| Plain dyna.tar | 2,689,075,200 | 2,033,827,840 |

The archive is now 492.12 MiB. This meets the 600 MB target.

The earlier measurements below include both agent CLIs.

The single final build reused the previous rootfs with --finalize-only and
nice -n 19. Both images use the 2026/10/15 package snapshot.

| Artifact | Before, bytes | After, bytes |
| --- | ---: | ---: |
| Shipped dyna.tar.zst | 752,751,674 | 706,590,233 |
| Plain dyna.tar | 2,870,405,120 | 2,689,075,200 |

The content cuts removed 181,329,920 bytes from the plain tar. The shipped
archive shrank by 46,161,441 bytes. With the agent CLIs, it missed the 600 MB target.

An XZ comparison produced 655,130,040 bytes (624.78 MiB). It also missed
600 MB. XZ saved 7.28 percent against the new Zstandard archive, but added
about 28 minutes of compression to this measured build.
XZ requested two threads with a 768 MiB memory cap. It used one thread to
stay within that cap. The XZ option was not adopted; the recipe ships only
Zstandard. The comparison command also returned a file-group warning after
compression because the source tar belongs to root. No second build ran.

Finalization makes these content cuts:

- Keep only English Chromium UI files. Keep ICU data and all fonts, so pages
  can still render other languages.
- Remove Chromedriver. The computer runtime uses Chromium's debugging
  protocol. Reinstall chromium with pacman if a workload needs WebDriver.
- Remove Python bytecode caches. Python recreates them from retained sources.
- Strip debug data from our two Go binaries.

The existing cleanup still removes package downloads, documentation, man
pages, and non-English system message catalogs. This does not set NoExtract;
pacman can reinstall any removed package files.

Keep LLVM and Mesa for llvmpipe and Intel/AMD rendering. Mesa's driver names
are symlinks, not duplicate payloads. Keep Python for vapoursynth, Perl for
git and hyphen, and binutils for libmakepkg-dropins. Keep WebKitGTK and GTK
for dyna-desktop and Chromium for computers. Keep
headers, static libraries, and i18n data for later package installs. There
is no kernel, firmware, mkinitcpio, gcc, or base-devel package to remove.
Never remove /var/lib/pacman or /usr/share/pacman/keyrings. Their file paths
and sizes are unchanged. The first boot service creates each machine's
private pacman keyring from those public keys.

Validation passed on the retained package snapshot: systemd, user manager,
passwordless sudo, pacman -Dk, the fresh private keyring, both agents,
headless Chromium, Python, Dyna HTTP service,
pixman labwc, foot fonts, grim, and wayvnc. A separate surfaceless EGL check
confirmed llvmpipe with LLVM 23.1.1. The Zstandard checksum and import passed;
imported LLVM, public keyring, and Dyna payload hashes matched the rootfs.
Each runtime check had a 9-second timeout in a user scope limited to
600 tasks and 2 GiB. The unique test containers were removed.
The test skipped the live first-boot package upgrade to retain the snapshot.

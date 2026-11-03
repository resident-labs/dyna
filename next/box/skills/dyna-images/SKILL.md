---
name: dyna-images
description: Use when your box lacks a tool, package, compiler or build target, or a command needs root, such as apt or sudo
---

# Change what a box has

A box runs as the image's own user, without root or `sudo`. Its tools come from the room's image, so `apt-get install` and writes to the image's folders, such as `/opt` or `/usr`, fail inside it by design.

## The image

A room keeps its images itself, not in the repository. Each is a folder with a `Containerfile` and what it copies in. Save one from a folder of yours:

- `dyna image save NAME PATH` (the `image` tool does the same), then `dyna image list`, `show NAME`, `remove NAME`.
- It starts `ARG DYNA_BASE` and `FROM ${DYNA_BASE}`, installs as `root`, and ends `USER node`.
- Optional files: `box-needs` (one need per line, such as `nested-containers` or `kvm`) and `size` (a default box size).
- A room with one box image uses it for every session. With several, a compose file names one: `build: NAME`.

Ask your parent first, since every session in the room gets it. A box takes a saved image the next time it starts. The first start on each host builds it, which can take several minutes. `session refresh` on yourself restarts your box at the end of your turn, with your work kept, so you continue on the new image.

Repositories that kept `.dyna/images/` were copied into their rooms once at upgrade. Changing the repository's folder no longer changes anything.

## Without changing the image

For one task, install into your home folder, which you can write:

- A Rust target: copy the toolchain to a folder of yours, point `RUSTUP_HOME` and `CARGO_HOME` at it, then `rustup target add`.
- A Debian package: `apt-get download PACKAGE` and `dpkg -x PACKAGE.deb ~/pkgs`, then add its `usr/bin` to `PATH`. A package with dependencies needs each one.
- Anything with a release tarball: unpack it under your home folder.

These last only as long as your box's volume. Save an image when the room will need the tool again.

## CI runner image

Your room's CI jobs run on Dyna's shared runner image. To add to it, such as a driver a test needs, save a room image named `ci-runner`. It starts from `${DYNA_CI_BASE}`, not `${DYNA_BASE}`:

```
ARG DYNA_CI_BASE
FROM ${DYNA_CI_BASE}
USER root
RUN apt-get update && apt-get install -y --no-install-recommends mesa-vulkan-drivers && rm -rf /var/lib/apt/lists/*
USER 1001:1001
```

Save it with `dyna image save ci-runner ./dir` (or the `image` tool). The repository's PRs cannot change it.

- You add layers on the shared image. Dyna sets the entrypoint, the user, the read-only root, no network and the dropped capabilities on every runner, so those lines in your file do nothing. Files you overwrite change only your own room's runners.
- The first build on each CI host takes minutes, and runs in the background, so the first job after a save may wait or run on the shared image. A later save builds again.
- If your file fails to build, the jobs use the shared image and `dyna host list` shows the host with the build's error. Fix it and save again.
- To see which image a job got, run `podman ps --filter label=dyna.ci` on the host: the image is `dyna-ci-room-<room>:<id>-<hash>` for yours and `dyna-ci-runner:<hash>` for the shared one. `dyna image show ci-runner` gives the id.

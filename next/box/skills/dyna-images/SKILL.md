---
name: dyna-images
description: Use when your box lacks a tool, package, compiler or build target, or a command needs root, such as apt or sudo
---

# Change what a box has

A box runs as the image's own user, without root or `sudo`. Its tools come from the repository's image, so `apt-get install` and writes to the image's folders, such as `/opt` or `/usr`, fail inside it by design.

## The image

Each repository's image is `.dyna/images/<name>/Containerfile`. It sits on the repository's default branch, or on a `dyna-images` branch when the default branch must stay clean, as for a fork that mirrors its upstream. It starts `FROM ${DYNA_BASE}`, installs as `root`, and ends as the box user.

To give every session a package, compiler or Rust target, change that Containerfile: an `apt-get install` line, a download, or a `--target` on the Rust install. On the default branch it changes by commit and PR, as the repository's code does. On `dyna-images` it is a commit to that branch. Ask your parent first, since every session in the room gets it.

A box takes the new image the next time it starts. The first start on each host builds the image, which can take several minutes. `session refresh` on yourself restarts your box at the end of your turn, with your work kept, so you continue on the new image.

## Without changing the image

For one task, install into your home folder, which you can write:

- A Rust target: copy the toolchain to a folder of yours, point `RUSTUP_HOME` and `CARGO_HOME` at it, then `rustup target add`.
- A Debian package: `apt-get download PACKAGE` and `dpkg -x PACKAGE.deb ~/pkgs`, then add its `usr/bin` to `PATH`. A package with dependencies needs each one.
- Anything with a release tarball: unpack it under your home folder.

These last only as long as your box's volume. Change the image when the room will need the tool again.

---
name: dyna-images
description: Use when your box lacks a tool, package or compiler, a command needs root (apt, sudo), or a child session needs a bigger box, a GPU, another image or a sidecar such as a database.
---

# Change what a box has

A box runs as user `node` (uid 1000) with no root and no `sudo`, so `apt-get install` and writes to `/usr` or `/opt`
fail by design. Its tools come from the room's box image, which every session of the room shares. A compose file lets
one child session choose an image and run sidecars, such as a database, beside its box.

## Get a tool for this task only

Install under your home folder (`$HOME` is `/box/home`, on your box's volume):

- A release tarball or a per-user installer: unpack under `~` and put its `bin` on `PATH`.
- A Debian package, with the package lists kept in your home folder:
  ```sh
  A="-o Dir::State::Lists=$HOME/apt/lists -o Dir::Cache=$HOME/apt/cache"
  mkdir -p ~/apt/lists/partial ~/apt/cache/archives/partial ~/deb && cd ~/deb
  apt-get $A update && apt-get $A download jq libjq1 libonig5
  for f in *.deb; do dpkg -x "$f" ~/pkgs; done
  export PATH=~/pkgs/usr/bin:$PATH LD_LIBRARY_PATH=~/pkgs/usr/lib/$(uname -m)-linux-gnu
  jq --version
  ```
  `apt-get download` fetches one package, not its dependencies; `apt-cache $A depends jq` lists them.
  Undo: `rm -rf ~/pkgs ~/deb ~/apt`; the exports end with the shell.

Files under `/box` survive a refresh and a new image. They are lost when the session is restarted with
`session restart`, and other sessions do not have them. When the room will need the tool again, change the image.

## Change the room's box image

Ask your parent first, since every session in the room gets the change; a root decides itself. Any session can save
to its own room, with the `image` tool or the `dyna image` CLI (`list`, `show NAME`, `save NAME PATH`, `remove NAME`).

1. Read what is there: `dyna image list`, then copy the current image to a folder, which is also your undo:
   `mkdir -p ~/img-old && dyna image show NAME | tail -n +3 > ~/img-old/Containerfile`
   (`show` prints a summary line, a `files:` line, then the Containerfile). Other files on the `files:` line are
   not printed, so with any, this copy is no full undo: keep the folder you save from as the next undo.
2. Write the new folder. A Containerfile has this shape; the save refuses any other `FROM`:
   ```dockerfile
   ARG DYNA_BASE
   FROM ${DYNA_BASE}
   USER root
   RUN apt-get update && apt-get install -y --no-install-recommends ripgrep && rm -rf /var/lib/apt/lists/*
   USER node
   ```
   Install outside `/box`: the box's volume is mounted there and hides what the image puts in it. Every stage
   starts `FROM ${DYNA_BASE}`. NAME is lowercase letters, digits, `_`, `.` and `-`. Only regular files are sent
   (links are dropped) and the folder packs to at most 3 MiB, within 16 images and 128 MiB per room, so download
   large toolchains in a `RUN` rather than copying them in.
   Optional files beside it: `box-needs` (words `nested-containers` or `kvm`; a box placed on a host without the
   device fails to start and placement does not look for it, so first have your parent restrict the room to hosts
   that have it (root: dyna-admin)) and `size` (a size from the table below, the default for boxes on this image).
3. Save it under the same name: `dyna image save NAME ~/img`. Saving an unchanged folder changes nothing.
4. Check it before you rely on it: `dyna image show NAME`, then run the new tool in a box on the new image. A box
   whose image does not build fails to start, so push your branch first, then either start a small child session
   whose brief is to run the tool and report, or `session refresh` yourself. The first start on each host builds
   the image, which takes minutes.
5. To roll it out, a root can call `session refresh` with its room; otherwise boxes take it at their next start.

Undo: `dyna image save NAME ~/img-old`, then refresh as in step 5. A session whose box failed on the broken image comes back only through
`session restart` by its parent, which discards its volume.

Keep one box image per room and extend it. With two or more, every box whose compose file has no `build`,
the root's included (a root takes no compose file), fails to start with "the room has several images, so a compose
file must choose one". `dyna image remove NAME` of the last box image returns the room to the plain base.

The CI runner image (`ci-runner`) is in dyna-ci.

## Start a child with a bigger box or a GPU

`session start` takes `size` and `gpu`. A box's size cannot change later; start a new session for more.

| Size | CPUs | Memory limit | Disk request |
|---|---:|---:|---:|
| agent | 1 | 3 GiB | 5 GiB |
| small | 2 | 6 GiB | 10 GiB |
| medium | 4 | 12 GiB | 20 GiB |
| large | 8 | 24 GiB | 40 GiB |
| xlarge | 16 | 48 GiB | 80 GiB |

Without `size`, a child of the root gets `medium` and any other child `agent`, unless its image has a `size` file.
A larger box waits longer for a host with room.

## Give a child another image or a database: compose files

A compose file names the image of a child's box (`build`) and adds sidecars. `session start` takes `compose` with
the file's name; without it the child uses the file named `default` if there is one, and otherwise starts plain.
A root's own box never uses one.

Where they live, most specific winning whole, with no merging:

| Level | Kept by |
|---|---|
| `.dyna/compose/NAME.yaml` in the child's repository, on its default branch | agents, through a normal PR |
| the room's compose folder | the owner; no command writes it |
| the global compose folder | the owner; no command writes it |

A file on your own branch is not read: it counts once it is merged. `session compose_list` shows each name a child
could use, its level, and the reason a start with it would be refused. After a merge, start the child with the name
given explicitly: a start fetches the repository once when it does not find that name yet.

```yaml
services:
  agent:                     # the child's box; this name is required
    build: rust              # a room image from `dyna image list`; leave it out in a room with one image
    environment:
      DATABASE_URL: postgres://postgres:dev@db:5432/postgres
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres:16-alpine
    environment:
      POSTGRES_PASSWORD: dev
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 2s
      retries: 20
    mem_limit: 1g
volumes:
  pgdata:
```

What a file may hold; any other key is refused with an error that names it:

| Where | Allowed |
|---|---|
| top level | `services`, `volumes` (names only, no settings) |
| `agent` | `build`, `environment`, `volumes`, `depends_on` |
| a sidecar | those, plus `image` or `build`, `command`, `healthcheck` (`test`, `interval`, `timeout`, `retries`, `start_period`), `cpus`, `mem_limit`, `pids_limit` |
| `depends_on` | `service_started` or `service_healthy`; nothing depends on `agent` |

- Values are literal: `$VAR` is refused (`$$` is a dollar sign); nothing passes in from the host. Variables Dyna
  sets, such as `HOME` or `CARGO_HOME`, are refused too. At most 64 KiB and 16 services.
- A sidecar's `image` comes from a public registry, pulled without sign-in. A sidecar `build` names a room image,
  which means a second box image (see above); prefer `image`.
- The box reaches each sidecar at `127.0.0.1` and by its service name. Sidecars share the box's memory and CPU limit,
  and no sidecar limit may pass the session's size, so size the child for both.
- Sidecars start in `depends_on` order, each healthcheck waited on for at most 2 minutes, and the box last. They stop
  and sleep with the box. One that dies is not restarted until the box next starts (`session refresh` on the child).
- When the file's sidecars or the box's image change, the next start replaces every container: data outside a named
  volume is lost. Named volumes belong to the box and go with `session restart` and `session close`.
- The child keeps the file's name, and every start reads and checks the file again: deleting or breaking it on the
  default branch keeps the child from starting until it is fixed.
